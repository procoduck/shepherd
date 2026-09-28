package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// FleetService implements mgmtv1connect.FleetServiceHandler. See
// docs/archive/api-contract-design.md, "Server wiring".
type FleetService struct {
	store  *store.Store
	logger *slog.Logger
	// schema drives signals.Derive when reconciling a collector's served
	// pipelines (GetReconciliation). Nil for the REST-shim FleetService, which
	// never serves that procedure; signals.Derive tolerates a nil registry
	// (unclassified → worst-case), so a nil here only ever affects
	// reconciliation, never the collector reads the shim uses.
	schema *schema.Registry
}

// FleetServiceOption configures optional FleetService behavior.
type FleetServiceOption func(*FleetService)

// WithFleetSchema supplies the schema registry GetReconciliation needs to derive
// a served pipeline's signals. The Connect handler wiring always passes it;
// tests that build a FleetService directly may leave it nil.
func WithFleetSchema(reg *schema.Registry) FleetServiceOption {
	return func(s *FleetService) { s.schema = reg }
}

// NewFleetService constructs a FleetService.
func NewFleetService(st *store.Store, logger *slog.Logger, opts ...FleetServiceOption) *FleetService {
	s := &FleetService{store: st, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ mgmtv1connect.FleetServiceHandler = (*FleetService)(nil)

// parseUUID parses s as a pgtype.UUID. ok is false for malformed input, in
// which case the returned value is the zero pgtype.UUID (Valid: false) —
// mirroring orgIDFromParam's behavior of treating a bad id as absent rather
// than propagating the scan error.
func parseUUID(s string) (id pgtype.UUID, ok bool) {
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}

// timestampFromPg converts a nullable timestamptz to a proto Timestamp, nil
// when unset — protojson renders a nil singular message field as an absent
// (or, with EmitUnpopulated, null) key, matching the legacy handlers'
// RFC3339-or-omitted string behavior closely enough for every existing
// assertion (see rpc_fleet_test.go and collectors_metadata_test.go).
func timestampFromPg(ts pgtype.Timestamptz) *timestamppb.Timestamp {
	if !ts.Valid {
		return nil
	}
	return timestamppb.New(ts.Time)
}

// structFromJSON decodes a jsonb column's raw bytes (collector/instance
// local_attributes, NOT NULL DEFAULT '{}') into a structpb.Struct. Empty
// input yields a nil Struct so the field renders absent, matching legacy's
// `omitempty` json.RawMessage behavior for the (never actually empty in
// practice, since the column is NOT NULL DEFAULT '{}') local_attributes field.
func structFromJSON(raw []byte) (*structpb.Struct, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	st := &structpb.Struct{}
	if err := protojson.Unmarshal(raw, st); err != nil {
		return nil, err
	}
	return st, nil
}

// ListCollectors lists collectors in the org named by the request. This is
// always scoped to that org, including for app admins: an app admin may
// access any org (see auth.authorizeOrgAccess), but which org's collectors
// come back is still governed by req.Msg.GetOrgId(), never "every org
// regardless of what was asked for" — every caller (OverviewPage,
// CollectorsPage, GitPage) passes the org the viewer currently has
// selected and relies on the response matching it.
func (s *FleetService) ListCollectors(ctx context.Context, req *connect.Request[mgmtv1.ListCollectorsRequest]) (*connect.Response[mgmtv1.ListCollectorsResponse], error) {
	orgID, ok := parseUUID(req.Msg.GetOrgId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}

	collectors, err := s.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list collectors"))
	}
	items := make([]*mgmtv1.Collector, len(collectors))
	for i := range collectors {
		c := &collectors[i]
		cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID)             //nolint:errcheck // empty name is safe
		summary, _ := s.store.Queries.GetLatestCollectorInstanceSummary(ctx, c.ID) //nolint:errcheck // zero value is safe default
		labels, labelErr := decodeCollectorLabels(c.Labels)
		if labelErr != nil {
			s.logger.Warn("list collectors: decoding inventory labels", "collector_id", c.ID.String(), "err", labelErr)
			labels = map[string]string{}
		}
		items[i] = &mgmtv1.Collector{
			Id:                 c.ID.String(),
			ClusterId:          c.ClusterID.String(),
			Cluster:            cluster.Name,
			Role:               c.Role,
			RemoteConfigStatus: summary.RemoteConfigStatus.String,
			LastSeen:           timestampFromPg(summary.LastSeen),
			AlloyVersion:       summary.AlloyVersion.String,
			Labels:             labels,
		}
	}
	return connect.NewResponse(&mgmtv1.ListCollectorsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org collector counts never approach int32 overflow
}

// GetCollector returns one collector, including its live instances.
func (s *FleetService) GetCollector(ctx context.Context, req *connect.Request[mgmtv1.GetCollectorRequest]) (*connect.Response[mgmtv1.Collector], error) {
	resp, err := s.getCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// loadOwnedCollector resolves a collector id and enforces that it belongs to
// orgIDStr, mirroring loadOwnedDestination/loadPipeline.
//
// The Connect interceptor cannot do this for us: it authorizes against the
// org NAMED IN THE REQUEST, which proves the caller
// has a role in that org and nothing about the id they passed alongside it. A
// by-id handler without this check is a cross-tenant read (or write) for any
// authenticated member of any org, because a UUID is not an authorization
// boundary.
//
// NotFound rather than PermissionDenied, deliberately: telling a caller that
// an id they cannot see nevertheless exists is itself a disclosure.
func (s *FleetService) loadOwnedCollector(ctx context.Context, orgIDStr, idStr string) (pgtype.UUID, error) {
	id, ok := parseUUID(idStr)
	if !ok {
		return pgtype.UUID{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid collector id"))
	}
	orgID, ok := parseUUID(orgIDStr)
	if !ok {
		return pgtype.UUID{}, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	owner, err := s.store.Queries.GetCollectorOrgID(ctx, id)
	if err != nil || !owner.Valid || owner != orgID {
		return pgtype.UUID{}, connect.NewError(connect.CodeNotFound, errors.New("collector not found"))
	}
	return id, nil
}

// getCollector is GetCollector's implementation. local_attributes travel as
// a structpb.Struct (the design's Struct-modeling rule for genuinely dynamic
// payloads), so numbers come back as float64: an integer beyond 2^53 or a
// decimal's trailing zeros are not preserved. The /api REST shim used to
// splice the stored bytes back in for byte-compatibility; it was removed in
// v0.11.0.
func (s *FleetService) getCollector(ctx context.Context, orgIDStr, idStr string) (*mgmtv1.Collector, error) {
	id, err := s.loadOwnedCollector(ctx, orgIDStr, idStr)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("collector not found"))
	}
	cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty name is safe

	rows, err := s.store.Queries.ListCollectorInstancesByCollector(ctx, id)
	if err != nil {
		s.logger.Warn("get collector: listing instances", "err", err)
		rows = nil
	}
	instances := make([]*mgmtv1.CollectorInstance, len(rows))
	for i := range rows {
		row := &rows[i]
		attrs, attrErr := structFromJSON(row.LocalAttributes)
		if attrErr != nil {
			s.logger.Warn("get collector: decoding instance local_attributes", "err", attrErr)
		}
		instances[i] = &mgmtv1.CollectorInstance{
			Name:               row.Name,
			AlloyVersion:       row.AlloyVersion.String,
			Os:                 row.Os.String,
			LastSeen:           timestampFromPg(row.LastSeen),
			RemoteConfigStatus: row.RemoteConfigStatus.String,
			RemoteConfigError:  row.RemoteConfigError.String,
			LocalAttributes:    attrs,
		}
	}
	resp := &mgmtv1.Collector{
		Id:        c.ID.String(),
		ClusterId: c.ClusterID.String(),
		Cluster:   cluster.Name,
		Role:      c.Role,
		Instances: instances,
	}
	resp.Labels, err = decodeCollectorLabels(c.Labels)
	if err != nil {
		return nil, err
	}
	if len(instances) > 0 {
		latest := instances[0]
		resp.RemoteConfigStatus = latest.RemoteConfigStatus
		resp.RemoteConfigError = latest.RemoteConfigError
		resp.LastSeen = latest.LastSeen
		resp.AlloyVersion = latest.AlloyVersion
		resp.LocalAttributes = latest.LocalAttributes
	}
	return resp, nil
}

func decodeCollectorLabels(raw []byte) (map[string]string, error) {
	labels := map[string]string{}
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to read collector labels"))
	}
	return labels, nil
}

func validCollectorLabelKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && !strings.ContainsRune("._-/", r) {
			return false
		}
	}
	return true
}

func validCollectorLabelValue(value string) bool {
	return value != "" && len(value) <= 512 && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) == -1
}

// SetCollectorLabel saves an inventory label independently of Alloy configuration.
func (s *FleetService) SetCollectorLabel(ctx context.Context, req *connect.Request[mgmtv1.SetCollectorLabelRequest]) (*connect.Response[mgmtv1.CollectorLabelsResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	key, value := strings.ToLower(req.Msg.GetKey()), req.Msg.GetValue()
	if !validCollectorLabelKey(key) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label key must be 1-128 bytes using lowercase letters, numbers, '.', '_', '-', or '/'"))
	}
	// #139: an admin label may not use a key the matcher reserves for a built-in
	// collector fact — otherwise, once labels become matcher keys, an admin label
	// could shadow (or be shadowed by) `cluster`/`role`/etc.
	if merge.IsReserved(key) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label key is reserved for a built-in collector attribute (cluster, role, id, os, alloy_version, or a collector.*/shepherd.* prefix)"))
	}
	if !validCollectorLabelValue(value) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label value must be 1-512 bytes with no control or format characters"))
	}
	// Read the prior labels before the write, as DeleteCollectorLabel does:
	// labels are not versioned, so the audit row is the only record that a
	// value changed. previous is empty when the key is new. beforeLabels
	// (the full map, not just this key) also feeds emitMatchDrift below.
	before, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	beforeLabels, decodeErr := decodeCollectorLabels(before.Labels)
	if decodeErr != nil {
		beforeLabels = map[string]string{}
	}
	previous := beforeLabels[key]
	raw, err := s.store.Queries.SetCollectorLabel(ctx, sqlc.SetCollectorLabelParams{
		ID: id, LabelKey: key, LabelValue: value,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, mapError(err)
		}
		if _, lookupErr := s.store.Queries.GetCollectorByID(ctx, id); lookupErr != nil {
			return nil, mapError(lookupErr)
		}
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("collector may have at most 64 labels"))
	}
	labels, err := decodeCollectorLabels(raw)
	if err != nil {
		return nil, err
	}
	orgID, _ := parseUUID(req.Msg.GetOrgId())
	auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", orgID, "collector.label.set", "collector", id.String(), map[string]string{"key": key, "value": value, "previous_value": previous})
	if cacheErr := s.store.Queries.MarkServeCacheDirty(ctx, id); cacheErr != nil {
		s.logger.Error("set collector label: marking serve cache dirty", "collector_id", id, "err", cacheErr)
	}
	s.emitMatchDrift(ctx, orgID, id, before.ClusterID, before.Role, beforeLabels, labels, "collector.label.set")
	return connect.NewResponse(&mgmtv1.CollectorLabelsResponse{Labels: labels}), nil
}

// DeleteCollectorLabel removes one inventory label without changing other labels.
func (s *FleetService) DeleteCollectorLabel(ctx context.Context, req *connect.Request[mgmtv1.DeleteCollectorLabelRequest]) (*connect.Response[mgmtv1.CollectorLabelsResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(req.Msg.GetKey())
	if !validCollectorLabelKey(key) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid label key"))
	}
	before, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	beforeLabels, decodeErr := decodeCollectorLabels(before.Labels)
	if decodeErr != nil {
		beforeLabels = map[string]string{}
	}
	previous := beforeLabels[key]
	raw, err := s.store.Queries.DeleteCollectorLabel(ctx, sqlc.DeleteCollectorLabelParams{
		ID: id, LabelKey: key,
	})
	if err != nil {
		return nil, mapError(err)
	}
	labels, err := decodeCollectorLabels(raw)
	if err != nil {
		return nil, err
	}
	orgID, _ := parseUUID(req.Msg.GetOrgId())
	auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", orgID, "collector.label.delete", "collector", id.String(), map[string]string{"key": key, "previous_value": previous})
	if cacheErr := s.store.Queries.MarkServeCacheDirty(ctx, id); cacheErr != nil {
		s.logger.Error("delete collector label: marking serve cache dirty", "collector_id", id, "err", cacheErr)
	}
	s.emitMatchDrift(ctx, orgID, id, before.ClusterID, before.Role, beforeLabels, labels, "collector.label.delete")
	return connect.NewResponse(&mgmtv1.CollectorLabelsResponse{Labels: labels}), nil
}

// emitMatchDrift is §7's match-drift observability hook (LABEL-MATCHING-PLAN.md),
// called after a label mutation commits. It recomputes which of the org's
// enabled pipelines match this collector under beforeLabels vs. afterLabels
// (the full admin-label maps, not just the one key that changed) and, for
// every pipeline whose match status flipped, increments
// metrics.PipelineMatchChangesTotal and writes a "pipeline.match.changed"
// audit row carrying the direction and cause.
//
// A no-op for an org with allow_label_matching off: admin labels never enter
// matching for such an org (every other BuildCollectorLabels call site passes
// nil for it, per adminLabelsIfAllowed), so a "flip" computed here would not
// reflect what's actually served — and skipping before listing pipelines
// avoids two extra queries on every label edit for the (currently common)
// case of an org that hasn't opted in.
//
// Best-effort: errors are logged, never returned — a label write must not
// fail because the drift computation that describes its downstream effect
// failed.
func (s *FleetService) emitMatchDrift(ctx context.Context, orgID, collectorID, clusterID pgtype.UUID, role string, beforeLabels, afterLabels map[string]string, cause string) {
	org, err := s.store.Queries.GetOrgByID(ctx, orgID)
	if err != nil || !org.AllowLabelMatching {
		return
	}
	rows, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		s.logger.Error("match-drift: listing enabled pipelines", "org_id", orgID.String(), "err", err)
		return
	}
	cluster, _ := s.store.Queries.GetClusterByID(ctx, clusterID) //nolint:errcheck // empty cluster name is safe in merge
	pipelines := make([]merge.Pipeline, 0, len(rows))
	for i := range rows {
		r := rows[i]
		var matchers []string
		if jsonErr := json.Unmarshal(r.Matchers, &matchers); jsonErr != nil {
			matchers = nil
		}
		pipelines = append(pipelines, merge.Pipeline{
			ID: r.ID.String(), Name: r.Name, Matchers: matchers, Source: r.Source,
			RepoLinkCollectorID: repoLinkCollectorID(r.RepoLinkCollectorID),
		})
	}
	collIDStr := collectorID.String()
	// localAttrs: nil — this hook is scoped to admin-label mutations only
	// (LABEL-MATCHING-PLAN.md PR-5); PR-9 adds the local_attributes-side
	// equivalent as its own hot-path-gated hook, not by threading local
	// attributes through this one.
	before := merge.BuildCollectorLabels(collIDStr, cluster.Name, role, beforeLabels, nil)
	after := merge.BuildCollectorLabels(collIDStr, cluster.Name, role, afterLabels, nil)
	for _, d := range merge.DiffMatches(pipelines, before, after) {
		metrics.PipelineMatchChangesTotal.WithLabelValues(d.Direction).Inc()
		auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", orgID, "pipeline.match.changed", "pipeline", d.PipelineID, map[string]string{
			"pipeline_id":   d.PipelineID,
			"pipeline_name": d.PipelineName,
			"collector_id":  collIDStr,
			"direction":     d.Direction,
			"cause":         cause,
		})
	}
}

// GetServedConfig returns the config currently served to a collector. A
// missing serve-cache row (never served yet) is not an error: it renders as
// an all-empty response, matching the pre-Connect REST handler's
// behavior of never surfacing the cache-miss as a 404.
func (s *FleetService) GetServedConfig(ctx context.Context, req *connect.Request[mgmtv1.GetServedConfigRequest]) (*connect.Response[mgmtv1.GetServedConfigResponse], error) {
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	cache, err := s.store.Queries.GetServeCache(ctx, id)
	if err != nil {
		return connect.NewResponse(&mgmtv1.GetServedConfigResponse{}), nil //nolint:nilerr // cache miss renders as an all-empty 200, matching legacy ServedConfig
	}
	return connect.NewResponse(&mgmtv1.GetServedConfigResponse{
		Content:    cache.Content,
		Hash:       cache.Hash,
		ComputedAt: timestampFromPg(cache.ComputedAt),
	}), nil
}

// ListAssignments lists the group assignments granting access to a
// collector, newest-display-name first (mirrors
// ListGroupAssignmentsByCollector's ORDER BY group_display_name).
func (s *FleetService) ListAssignments(ctx context.Context, req *connect.Request[mgmtv1.ListAssignmentsRequest]) (*connect.Response[mgmtv1.ListAssignmentsResponse], error) {
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListGroupAssignmentsByCollector(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list assignments"))
	}
	items := make([]*mgmtv1.Assignment, len(rows))
	for i := range rows {
		a := &rows[i]
		items[i] = &mgmtv1.Assignment{
			Id:               a.ID.String(),
			GroupId:          a.GroupID,
			GroupDisplayName: a.GroupDisplayName,
			CreatedAt:        timestampFromPg(a.CreatedAt),
		}
	}
	return connect.NewResponse(&mgmtv1.ListAssignmentsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // assignment counts per collector never approach int32 overflow
}

// CreateAssignment assigns a group to a collector.
func (s *FleetService) CreateAssignment(ctx context.Context, req *connect.Request[mgmtv1.CreateAssignmentRequest]) (*connect.Response[mgmtv1.CreateAssignmentResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	a, err := s.store.Queries.CreateGroupAssignment(ctx, sqlc.CreateGroupAssignmentParams{
		CollectorID:      id,
		GroupID:          req.Msg.GetGroupId(),
		GroupDisplayName: req.Msg.GetGroupDisplayName(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create assignment"))
	}
	return connect.NewResponse(&mgmtv1.CreateAssignmentResponse{Id: a.ID.String(), GroupId: a.GroupID}), nil
}

// DeleteAssignment removes a group assignment from a collector.
func (s *FleetService) DeleteAssignment(ctx context.Context, req *connect.Request[mgmtv1.DeleteAssignmentRequest]) (*connect.Response[mgmtv1.DeleteAssignmentResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	collID, err := s.loadOwnedCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.DeleteGroupAssignment(ctx, sqlc.DeleteGroupAssignmentParams{
		CollectorID: collID,
		GroupID:     req.Msg.GetGroupId(),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete assignment"))
	}
	return connect.NewResponse(&mgmtv1.DeleteAssignmentResponse{}), nil
}

// ListAttributes returns the matcher keys pipeline matching evaluates for an
// org, each with its distinct values: the suggestions behind the pipeline
// editor's matcher input and the MCP list_fleet_attributes tool. That is
// exactly what merge.BuildCollectorLabels sees (#139): cluster and role
// always; admin labels only when the org has allow_label_matching; agent
// local_attributes (latest instance per collector, keys lowercased) only
// with allow_local_attribute_matching; reserved keys never. A key matching
// would ignore is not suggested, because a matcher written against it can
// never hit. A malformed/empty org_id resolves to no collectors.
func (s *FleetService) ListAttributes(ctx context.Context, req *connect.Request[mgmtv1.ListAttributesRequest]) (*connect.Response[mgmtv1.ListAttributesResponse], error) {
	orgID, _ := parseUUID(req.Msg.GetOrgId()) // invalid/empty org id resolves to NULL: no rows
	collectors, err := s.store.Queries.ListCollectorsWithClusterByOrg(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list attributes"))
	}
	org, _ := s.store.Queries.GetOrgByID(ctx, orgID) //nolint:errcheck // a missing org degrades to cluster/role only, as on the serve paths

	values := map[string]map[string]struct{}{"cluster": {}, "role": {}}
	add := func(k, v string) {
		if values[k] == nil {
			values[k] = map[string]struct{}{}
		}
		values[k][v] = struct{}{}
	}
	localAttrs := localAttrsByOrg(ctx, s.store.Queries, orgID, org.AllowLocalAttributeMatching)
	for i := range collectors {
		c := collectors[i]
		// BuildCollectorLabels applies the reserved-key filter and the
		// lowercasing, so suggestions cannot drift from matching.
		cl := merge.BuildCollectorLabels(c.ID.String(), c.ClusterName, c.Role,
			adminLabelsIfAllowed(org.AllowLabelMatching, c.Labels), localAttrs[c.ID.String()])
		for k, v := range cl.Labels {
			add(k, v)
		}
	}

	result := make(map[string]any, len(values))
	for k, set := range values {
		vals := make([]string, 0, len(set))
		for v := range set {
			vals = append(vals, v)
		}
		slices.Sort(vals)
		list := make([]any, len(vals))
		for i, v := range vals {
			list[i] = v
		}
		result[k] = list
	}
	attrs, err := structpb.NewStruct(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to encode attributes"))
	}
	return connect.NewResponse(&mgmtv1.ListAttributesResponse{Attributes: attrs}), nil
}
