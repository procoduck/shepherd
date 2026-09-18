package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// FleetService implements mgmtv1connect.FleetServiceHandler. Business logic
// migrated here from OrgsHandler's collector/assignment/attribute methods
// (orgs.go), which are now thin REST shims delegating to these methods
// in-process. See docs/archive/api-contract-design.md, "Server wiring".
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
// a served pipeline's signals. Only the Connect handler wiring passes it; the
// REST shim (orgs.go) leaves it nil.
func WithFleetSchema(reg *schema.Registry) FleetServiceOption {
	return func(s *FleetService) { s.schema = reg }
}

// NewFleetService constructs a FleetService with the deps OrgsHandler uses today.
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
	resp, _, err := s.getCollector(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// collectorLocalAttrsRaw is the collector/instance jsonb local_attributes
// columns' raw bytes, exactly as stored, alongside the *mgmtv1.Collector
// getCollector builds from them. The Connect wire response only ever
// carries LocalAttributes as a structpb.Struct (per the design's
// Struct-modeling rule for genuinely dynamic payloads); decoding stored
// JSON into a Struct and re-marshaling it through protojson is not
// byte-preserving — map iteration order isn't the original key order, and
// every number becomes a Struct Value's float64 — so the REST shim (which
// must stay byte-compatible with the legacy handler's direct
// json.RawMessage passthrough) substitutes these raw bytes back in after
// marshaling. See orgs.go's GetCollector.
type collectorLocalAttrsRaw struct {
	collector json.RawMessage   // nil when the collector has no reporting instances
	instances []json.RawMessage // parallel to Collector.Instances; element nil if that instance's attrs failed to decode
}

// loadOwnedCollector resolves a collector id and enforces that it belongs to
// orgIDStr, mirroring loadOwnedDestination/loadPipeline.
//
// Neither the Connect interceptor nor the REST middleware can do this for us:
// both authorize against the org NAMED IN THE REQUEST, which proves the caller
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

// getCollector is GetCollector's implementation, additionally returning the
// untouched local_attributes bytes the REST shim needs for byte-compatible
// rendering (collectorLocalAttrsRaw) — kept out of the exported Connect
// method so the wire contract (a bare *mgmtv1.Collector) is unaffected.
func (s *FleetService) getCollector(ctx context.Context, orgIDStr, idStr string) (*mgmtv1.Collector, collectorLocalAttrsRaw, error) {
	id, err := s.loadOwnedCollector(ctx, orgIDStr, idStr)
	if err != nil {
		return nil, collectorLocalAttrsRaw{}, err
	}
	c, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, collectorLocalAttrsRaw{}, connect.NewError(connect.CodeNotFound, errors.New("collector not found"))
	}
	cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty name is safe

	rows, err := s.store.Queries.ListCollectorInstancesByCollector(ctx, id)
	if err != nil {
		s.logger.Warn("get collector: listing instances", "err", err)
		rows = nil
	}
	instances := make([]*mgmtv1.CollectorInstance, len(rows))
	rawAttrs := make([]json.RawMessage, len(rows))
	for i := range rows {
		row := &rows[i]
		attrs, attrErr := structFromJSON(row.LocalAttributes)
		if attrErr != nil {
			s.logger.Warn("get collector: decoding instance local_attributes", "err", attrErr)
		} else if len(row.LocalAttributes) > 0 {
			rawAttrs[i] = row.LocalAttributes
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
	raw := collectorLocalAttrsRaw{instances: rawAttrs}
	resp.Labels, err = decodeCollectorLabels(c.Labels)
	if err != nil {
		return nil, collectorLocalAttrsRaw{}, err
	}
	if len(instances) > 0 {
		latest := instances[0]
		resp.RemoteConfigStatus = latest.RemoteConfigStatus
		resp.RemoteConfigError = latest.RemoteConfigError
		resp.LastSeen = latest.LastSeen
		resp.AlloyVersion = latest.AlloyVersion
		resp.LocalAttributes = latest.LocalAttributes
		raw.collector = rawAttrs[0]
	}
	return resp, raw, nil
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

// reservedCollectorLabelKeys are the keys a pipeline matcher reserves for
// built-in collector facts (#139). `cluster` and `role` are what the matcher
// already means today; `id`, `os` and `alloy_version` are identity/agent facts
// it will expose as labels once collectors.labels and local_attributes become
// matcher keys. An admin-set label may not use any of them, so an admin label
// can never shadow — or be shadowed by — a built-in matcher key. The
// `collector.` / `shepherd.` prefixes are reserved alongside them (see
// reservedCollectorLabelKey) so future built-in keys never become a breaking
// change for an org that had picked the same name.
var reservedCollectorLabelKeys = map[string]struct{}{
	"cluster":       {},
	"role":          {},
	"id":            {},
	"os":            {},
	"alloy_version": {},
}

// reservedCollectorLabelKey reports whether key is reserved for a built-in
// collector attribute — an exact reserved key, or anything under the reserved
// `collector.` / `shepherd.` namespaces. Callers reject a write to such a key.
func reservedCollectorLabelKey(key string) bool {
	if _, ok := reservedCollectorLabelKeys[key]; ok {
		return true
	}
	return strings.HasPrefix(key, "collector.") || strings.HasPrefix(key, "shepherd.")
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
	if reservedCollectorLabelKey(key) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label key is reserved for a built-in collector attribute (cluster, role, id, os, alloy_version, or a collector.*/shepherd.* prefix)"))
	}
	if !validCollectorLabelValue(value) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("label value must be 1-512 bytes with no control or format characters"))
	}
	// Read the prior value before the write, as DeleteCollectorLabel does:
	// labels are not versioned, so the audit row is the only record that a
	// value changed. previous is empty when the key is new.
	before, err := s.store.Queries.GetCollectorByID(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	previous := ""
	if current, decodeErr := decodeCollectorLabels(before.Labels); decodeErr == nil {
		previous = current[key]
	}
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
	previous := ""
	if current, decodeErr := decodeCollectorLabels(before.Labels); decodeErr == nil {
		previous = current[key]
	}
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
	return connect.NewResponse(&mgmtv1.CollectorLabelsResponse{Labels: labels}), nil
}

// GetServedConfig returns the config currently served to a collector. A
// missing serve-cache row (never served yet) is not an error: it renders as
// an all-empty response, matching OrgsHandler.ServedConfig's existing
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

// ListAttributes lists local attributes observed across an org's
// collectors, keyed by attribute name. A malformed/empty org_id resolves to
// SQL NULL (matching legacy orgIDFromParam, which never rejected it) and the
// query simply returns no rows for it.
func (s *FleetService) ListAttributes(ctx context.Context, req *connect.Request[mgmtv1.ListAttributesRequest]) (*connect.Response[mgmtv1.ListAttributesResponse], error) {
	orgID, _ := parseUUID(req.Msg.GetOrgId()) // invalid/empty org id resolves to NULL, matching legacy orgIDFromParam
	keys, err := s.store.Queries.ListDistinctAttributeKeys(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list attributes"))
	}
	result := map[string]any{"cluster": []any{}, "role": []any{}}
	for _, k := range keys {
		vals, _ := s.store.Queries.ListDistinctAttributeValues(ctx, sqlc.ListDistinctAttributeValuesParams{ //nolint:errcheck // empty is safe fallback
			OrgID:   orgID,
			Column2: k,
		})
		valAny := make([]any, len(vals))
		for i, v := range vals {
			valAny[i] = v
		}
		result[k] = valAny
	}
	attrs, err := structpb.NewStruct(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to encode attributes"))
	}
	return connect.NewResponse(&mgmtv1.ListAttributesResponse{Attributes: attrs}), nil
}
