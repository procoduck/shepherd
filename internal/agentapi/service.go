package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/schema"
	"shepherd/internal/serve"
	"shepherd/internal/signals"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// EmptyHash is sha256hex(""). Exported for use in tests.
const EmptyHash = "e3b0c44298fc1c149afbf4c8996fb924" +
	"27ae41e4649b934ca495991b7852b855"

// emptyHash is an unexported alias kept for backward compat inside this package.
const emptyHash = EmptyHash

// Service implements collector.v1 CollectorService.
type Service struct {
	collectorv1connect.UnimplementedCollectorServiceHandler
	store     *store.Store
	validator *validate.Validator
	logger    *slog.Logger
	sf        singleflight.Group
	// schema drives signal/role enforcement on the lazy recompute path. This
	// path and internal/mgmtapi's eager one produce the SAME served config, so
	// enforcing only one of them would not be enforcement at all — it would
	// just move the hole. Nil disables enforcement here (see New).
	schema *schema.Registry
	// beaconBaseline configures D6's baseline pipeline, appended to every
	// claimed collector's served config by recomputeServeCache — see
	// WithBeaconRemoteWrite. Zero value (RemoteWriteURL == "") means
	// disabled; beacon.AppendBaseline treats that as "leave content
	// unchanged", never as an error.
	beaconBaseline beacon.BaselineConfig
}

// ServiceOption configures optional Service behavior not required by every
// caller (server.go's production wiring vs. a test constructing a minimal
// Service) — the same variadic-option shape internal/merge.AssembleOption
// already uses for the same reason (WithRoleEnforcement).
type ServiceOption func(*Service)

// WithBeaconRemoteWrite enables D6's baseline pipeline on the lazy recompute
// path: every claimed collector's served config gets
// beacon.RenderBaselinePipeline appended, pointed at baseURL+BeaconWritePath.
// baseURL is config.ServerConfig.BaseURL; passing "" is equivalent to not
// applying this option at all (beacon.AppendBaseline's documented no-op).
func WithBeaconRemoteWrite(baseURL string, oauth2 *beacon.OAuth2Auth) ServiceOption {
	return func(s *Service) {
		remoteWriteURL := ""
		if baseURL != "" {
			remoteWriteURL = strings.TrimSuffix(baseURL, "/") + beacon.WritePath
		}
		cfg := beacon.NewBaselineConfig(remoteWriteURL)
		if oauth2 != nil && remoteWriteURL != "" {
			cfg.OAuth2 = oauth2
		}
		s.beaconBaseline = cfg
	}
}

// New creates a new agent API Service. reg may be nil, in which case the lazy
// recompute path serves without role enforcement — a corrupt embedded schema
// degrades the guard rather than taking the fleet offline. Callers that can
// load a registry must pass it.
func New(st *store.Store, v *validate.Validator, logger *slog.Logger, reg *schema.Registry, opts ...ServiceOption) *Service {
	s := &Service{store: st, validator: v, logger: logger.With("component", "agentapi"), schema: reg}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// RegisterCollector handles collector registration.
func (s *Service) RegisterCollector(
	ctx context.Context,
	req *connect.Request[collectorv1.RegisterCollectorRequest],
) (*connect.Response[collectorv1.RegisterCollectorResponse], error) {
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Alloy's collector.register RPC sends no name field, so fall back to
	// the wire id — matching GetConfig's fallback (§4.1) — rather than
	// storing an empty string that nothing else ever heals.
	name := req.Msg.Name
	if name == "" {
		name = req.Msg.Id
	}
	if err := s.upsertCollectorInstance(ctx, req.Msg.Id, name, cluster, role, attrs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&collectorv1.RegisterCollectorResponse{}), nil
}

// GetConfig handles config polling by collectors.
func (s *Service) GetConfig(
	ctx context.Context,
	req *connect.Request[collectorv1.GetConfigRequest],
) (*connect.Response[collectorv1.GetConfigResponse], error) {
	// Every success path records its outcome through metrics.ObserveGetConfig,
	// which pairs the counter with the latency histogram. They were separate
	// before, and the histogram was never observed at any of the four return
	// points, so shepherd_getconfig_duration_seconds never appeared in
	// /metrics at all.
	start := time.Now()
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Upsert instance (self-register on GetConfig too — §4.1). GetConfigRequest
	// carries no dedicated name field, so prefer a name reported via attributes;
	// otherwise fall back to the wire id, matching first-registration behavior.
	// Either way, the SQL upsert preserves any existing non-wire-id display
	// name rather than clobbering it with the wire id on every subsequent poll.
	name := attrs["collector.name"]
	if name == "" {
		name = req.Msg.Id
	}

	// PR-9's match-drift short-circuit needs the instance's PREVIOUS
	// local_attributes before upsertCollectorInstance overwrites it. This
	// read runs on every poll, so it must stay cheap: one extra SELECT, never
	// a pipeline list + diff, for the overwhelmingly common case of unchanged
	// attributes — see emitLocalAttrsMatchDrift's doc comment for why the
	// comparison itself is over decoded maps, not raw jsonb bytes.
	var beforeAttrsJSON json.RawMessage
	if prev, prevErr := s.store.Queries.GetCollectorInstanceByID(ctx, req.Msg.Id); prevErr == nil {
		beforeAttrsJSON = prev.LocalAttributes
	}

	if err := s.upsertCollectorInstance(ctx, req.Msg.Id, name, cluster, role, attrs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Persist remote_config_status if provided.
	if req.Msg.RemoteConfigStatus != nil {
		statusStr := strings.TrimPrefix(req.Msg.RemoteConfigStatus.Status.String(), "RemoteConfigStatuses_")
		errMsg := req.Msg.RemoteConfigStatus.ErrorMessage
		if err := s.store.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
			ID:                 req.Msg.Id,
			RemoteConfigStatus: pgtype.Text{String: statusStr, Valid: true},
			RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
			// Which config this status is about (#115): the hash sent with it.
			StatusHash: pgtype.Text{String: req.Msg.Hash, Valid: req.Msg.Hash != ""},
		}); err != nil {
			s.logger.Warn("failed to update instance status", "instance_id", req.Msg.Id, "err", err)
		}
	}

	// Resolve collector and check if cluster is claimed.
	coll, err := s.store.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{
		Name: cluster,
		Role: role,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving collector: %w", err))
	}

	orgID, err := s.resolveOrg(ctx, cluster, role, coll)
	if err != nil {
		return nil, err
	}

	// Unclaimed cluster: serve empty config.
	if !orgID.Valid {
		s.applySilentPoll(ctx, req.Msg, emptyHash)
		if req.Msg.Hash == emptyHash {
			metrics.ObserveGetConfig("not_modified", start)
			return connect.NewResponse(&collectorv1.GetConfigResponse{
				NotModified: true,
				Hash:        emptyHash,
			}), nil
		}
		metrics.ObserveGetConfig("served", start)
		return connect.NewResponse(&collectorv1.GetConfigResponse{
			Content:     "",
			Hash:        emptyHash,
			NotModified: false,
		}), nil
	}

	s.emitLocalAttrsMatchDrift(ctx, orgID, coll, cluster, role, beforeAttrsJSON, attrs)

	// Claimed: read from serve cache; recompute if dirty.
	cache, err := s.store.Queries.GetServeCache(ctx, coll.ID)
	if err != nil || cache.Dirty {
		// The generation this poll observed, captured BEFORE the pipelines
		// are loaded inside recomputeServeCache: the upsert below is a
		// compare-and-swap on it, so a mark that lands mid-recompute (an
		// enable, a restore, a git sync) makes this write a no-op instead of
		// letting stale content clear the newer dirty flag. No row yet means
		// generation 0.
		var expectedSeq int64
		if err == nil {
			expectedSeq = cache.DirtySeq
		}
		// Cache missing or dirty — recompute now, once per collector at a time.
		result, recomputeErr, _ := s.sf.Do(coll.ID.String(), func() (any, error) {
			newContent, newHash, err := s.recomputeServeCache(ctx, coll, orgID, attrs)
			if err != nil {
				return nil, err
			}
			updated, err := s.store.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: coll.ID,
				Content:     newContent,
				Hash:        newHash,
				DirtySeq:    expectedSeq,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				// Superseded: a newer mark arrived while this recompute ran,
				// or the eager recompute already served this generation.
				// Serve whatever the row holds now — if it is still dirty,
				// the next poll recomputes against the newer generation.
				return s.store.Queries.GetServeCache(ctx, coll.ID)
			}
			if err != nil {
				return nil, err
			}
			return updated, nil
		})
		if recomputeErr != nil {
			metrics.ServeRecomputeFailuresTotal.Inc()
			s.logger.Warn("serve-cache recompute failed", "collector_id", coll.ID.String(), "err", recomputeErr)
			// Fall through: serve whatever is in cache (or empty if no cache yet).
		} else {
			cache = result.(sqlc.ServeCache)
		}
	}

	// A poll with no fresh status but the hash we served: see applySilentPoll.
	s.applySilentPoll(ctx, req.Msg, cache.Hash)

	if req.Msg.Hash == cache.Hash {
		metrics.ObserveGetConfig("not_modified", start)
		return connect.NewResponse(&collectorv1.GetConfigResponse{
			NotModified: true,
			Hash:        cache.Hash,
		}), nil
	}

	metrics.ObserveGetConfig("served", start)
	return connect.NewResponse(&collectorv1.GetConfigResponse{
		Content:     cache.Content,
		Hash:        cache.Hash,
		NotModified: false,
	}), nil
}

// applySilentPoll records what a status-less poll for the served hash means.
// Alloy re-sends a status only when its (status, error) pair changes, so the
// silence is "same outcome as last reported" — for a new config too (#115,
// F1: a new config failing with an identical error is never re-reported). A
// poll carrying effective_config is the exception that proves a load: Alloy
// sets it only after loading successfully. The SQL (ApplySilentPoll) holds
// the full rule; a status carried by the request was already persisted above
// and always wins, so this is a no-op then.
func (s *Service) applySilentPoll(ctx context.Context, req *collectorv1.GetConfigRequest, servedHash string) {
	if req.GetRemoteConfigStatus() != nil || req.GetHash() != servedHash {
		return
	}
	loaded := len(req.GetEffectiveConfig().GetConfigMap().GetConfigMap()) > 0
	if err := s.store.Queries.ApplySilentPoll(ctx, sqlc.ApplySilentPollParams{
		ID:         req.GetId(),
		Loaded:     loaded,
		PolledHash: pgtype.Text{String: req.GetHash(), Valid: req.GetHash() != ""},
	}); err != nil {
		s.logger.Warn("failed to record silent poll", "instance_id", req.GetId(), "err", err)
	}
}

// UnregisterCollector marks an instance as unregistered.
func (s *Service) UnregisterCollector(
	ctx context.Context,
	req *connect.Request[collectorv1.UnregisterCollectorRequest],
) (*connect.Response[collectorv1.UnregisterCollectorResponse], error) {
	if err := s.store.Queries.UnregisterInstance(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&collectorv1.UnregisterCollectorResponse{}), nil
}

// upsertCollectorInstance upserts cluster → collector → instance rows.
func (s *Service) upsertCollectorInstance(
	ctx context.Context,
	instanceID, name, cluster, role string,
	attrs map[string]string,
) error {
	cl, err := s.store.Queries.UpsertCluster(ctx, cluster)
	if err != nil {
		return fmt.Errorf("upserting cluster %q: %w", cluster, err)
	}

	coll, err := s.store.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{
		ClusterID: cl.ID,
		Role:      role,
	})
	if err != nil {
		return fmt.Errorf("upserting collector: %w", err)
	}

	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("marshaling attributes: %w", err)
	}

	alloyVersion := pgtype.Text{}
	if v, ok := attrs["collector.version"]; ok {
		alloyVersion = pgtype.Text{String: v, Valid: true}
	}
	osVal := pgtype.Text{}
	if v, ok := attrs["collector.os"]; ok {
		osVal = pgtype.Text{String: v, Valid: true}
	}

	if _, err := s.store.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
		ID:              instanceID,
		CollectorID:     coll.ID,
		Name:            name,
		LocalAttributes: attrsJSON,
		AlloyVersion:    alloyVersion,
		Os:              osVal,
	}); err != nil {
		return fmt.Errorf("upserting instance: %w", err)
	}

	return nil
}

// emitLocalAttrsMatchDrift is PR-9's local_attributes half of §7's match-drift
// observability (LABEL-MATCHING-PLAN.md), mirroring FleetService.emitMatchDrift's
// admin-label version but hooked on the agent's own heartbeat instead of a
// mgmtapi write — see rpc_fleet.go's emitMatchDrift doc comment for why that
// hook stays label-only rather than growing a local_attrs parameter.
//
// NOT unconditional: GetConfig calls this on every poll, up to 1000 collectors
// every 30-60s. The compare against the previous stored value (done here,
// before any other work) is what keeps this cheap — checked FIRST, so the
// overwhelmingly common case (attributes unchanged since the last poll) costs
// nothing beyond the caller's one extra SELECT plus unmarshaling two small
// maps, never a pipeline list + Assemble diff.
//
// This compares maps, not raw JSON bytes: local_attributes is a Postgres
// jsonb column, which reformats on every round trip (Postgres's own
// canonical jsonb text output, not byte-identical to Go's compact
// json.Marshal) — a byte comparison between what GetCollectorInstanceByID
// reads back and what json.Marshal just produced would report "changed" on
// every single poll, even when nothing changed, defeating the short-circuit
// entirely. maps.Equal is immune to that: it compares decoded content, and
// costs no more than one JSON unmarshal beyond what this function already
// needs to build the before/after CollectorLabels below.
//
// Best-effort: errors are logged, never returned — a poll must not fail
// because the drift computation describing its downstream effect failed.
func (s *Service) emitLocalAttrsMatchDrift(ctx context.Context, orgID pgtype.UUID, coll sqlc.Collector, cluster, role string, beforeAttrsJSON json.RawMessage, afterAttrs map[string]string) {
	var beforeAttrs map[string]string
	if len(beforeAttrsJSON) > 0 {
		_ = json.Unmarshal(beforeAttrsJSON, &beforeAttrs) //nolint:errcheck // malformed prior value degrades to no local attrs
	}
	if maps.Equal(beforeAttrs, afterAttrs) {
		return
	}
	org, err := s.store.Queries.GetOrgByID(ctx, orgID)
	if err != nil || !org.AllowLocalAttributeMatching {
		// A no-op for an org with the flag off: local_attributes never enter
		// matching for such an org (every BuildCollectorLabels call site gates
		// it the same way), so a "flip" computed here would not reflect what's
		// actually served.
		return
	}
	rows, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		s.logger.Error("match-drift: listing enabled pipelines", "org_id", orgID.String(), "err", err)
		return
	}
	pipelines := make([]merge.Pipeline, 0, len(rows))
	for i := range rows {
		r := rows[i]
		var matchers []string
		if jsonErr := json.Unmarshal(r.Matchers, &matchers); jsonErr != nil {
			matchers = nil
		}
		repoLinkCollectorID := ""
		if r.RepoLinkCollectorID.Valid {
			repoLinkCollectorID = r.RepoLinkCollectorID.String()
		}
		pipelines = append(pipelines, merge.Pipeline{
			ID: r.ID.String(), Name: r.Name, Matchers: matchers, Source: r.Source,
			RepoLinkCollectorID: repoLinkCollectorID,
		})
	}
	// Admin labels held constant across before/after: only local_attrs changed
	// here, and the diff should reflect that in isolation, same as
	// emitMatchDrift holds local_attrs constant (nil) for an admin-label edit.
	var adminLabels map[string]string
	if org.AllowLabelMatching {
		if jsonErr := json.Unmarshal(coll.Labels, &adminLabels); jsonErr != nil {
			adminLabels = nil
		}
	}
	collIDStr := coll.ID.String()
	before := merge.BuildCollectorLabels(collIDStr, cluster, role, adminLabels, beforeAttrs)
	after := merge.BuildCollectorLabels(collIDStr, cluster, role, adminLabels, afterAttrs)
	for _, d := range merge.DiffMatches(pipelines, before, after) {
		metrics.PipelineMatchChangesTotal.WithLabelValues(d.Direction).Inc()
		detail, _ := json.Marshal(map[string]string{ //nolint:errcheck // map[string]string always marshals
			"pipeline_id":   d.PipelineID,
			"pipeline_name": d.PipelineName,
			"collector_id":  collIDStr,
			"direction":     d.Direction,
			"cause":         "collector.local_attributes.report",
		})
		if auditErr := s.store.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "agentapi", ActorType: "system", OrgID: orgID,
			Action: "pipeline.match.changed", ResourceType: "pipeline", ResourceID: d.PipelineID,
			Detail: detail,
		}); auditErr != nil {
			s.logger.Error("match-drift: writing audit log", "pipeline_id", d.PipelineID, "err", auditErr)
		}
	}
}

// effectiveAttrs returns local_attributes, falling back to deprecated attributes if empty.
// resolveOrg decides which org a request's config is served for, applying the
// collector-OIDC resolution chain (docs/archive/plans/2026-09-16-agent-oidc-auth.md D1):
//
//  1. An OIDC principal with an agent_identities binding on its (issuer,
//     app_id): the binding's org, after enforcing its optional cluster/role
//     allowlists, and auto-claiming the cluster to that org (refusing a
//     cluster already claimed by another org).
//  2. Anything else — an agent-token principal, or an OIDC principal with no
//     binding: the existing model, where org comes from an admin cluster-claim
//     (GetCollectorOrgID; an unclaimed cluster resolves to a null org and is
//     served empty upstream).
func (s *Service) resolveOrg(ctx context.Context, cluster, role string, coll sqlc.Collector) (pgtype.UUID, error) {
	p := PrincipalFrom(ctx)
	if p.Kind == AuthKindOIDC && p.Claims != nil {
		binding, err := s.store.Queries.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{
			Issuer: p.Claims.Issuer,
			AppID:  p.Claims.AppID,
		})
		switch {
		case err == nil:
			return s.resolveBoundOrg(ctx, cluster, role, coll, binding)
		case errors.Is(err, pgx.ErrNoRows):
			// No binding. If mode 2 is on and the token asserts an org (D1
			// tier 2), trust it; otherwise OIDC proved liveness only and we
			// fall through to the admin cluster-claim model below.
			if p.Claims.Org != "" {
				return s.resolveClaimOrg(ctx, cluster, coll, p.Claims)
			}
		default:
			return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving agent identity: %w", err))
		}
	}
	orgID, err := s.store.Queries.GetCollectorOrgID(ctx, coll.ID)
	if err != nil {
		return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving org: %w", err))
	}
	return orgID, nil
}

// resolveBoundOrg enforces an agent_identities binding's cluster/role
// allowlists and auto-claims the cluster to the binding's org.
func (s *Service) resolveBoundOrg(ctx context.Context, cluster, role string, coll sqlc.Collector, binding sqlc.AgentIdentity) (pgtype.UUID, error) {
	if !allowlistPermits(binding.Clusters, cluster) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is not permitted for this collector identity", cluster))
	}
	if !allowlistPermits(binding.Roles, role) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("role %q is not permitted for this collector identity", role))
	}
	// Bind the cluster to the identity's org, or confirm it is already ours.
	// A cluster claimed by a different org matches no row (ErrNoRows) — a
	// token can never take over another org's cluster by naming it.
	claimed, err := s.store.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{
		ID:    coll.ClusterID,
		OrgID: binding.OrgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is claimed by another organisation", cluster))
	}
	if err != nil {
		return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("claiming cluster: %w", err))
	}
	return claimed, nil
}

// resolveClaimOrg handles D1 tier 2: the token carries no Shepherd binding but
// the IdP asserts an org (mode 2, opted into via config). The asserted value is
// an org NAME — it is resolved to an id here, and the cluster is auto-claimed
// to it exactly as a binding would, refusing a cluster already owned by another
// org. An asserted org that does not exist is a hard refusal, not a
// fall-through: the operator has chosen to trust this issuer's org assignment,
// so an unknown org names a real misconfiguration rather than "serve empty".
func (s *Service) resolveClaimOrg(ctx context.Context, cluster string, coll sqlc.Collector, claims *auth.AgentClaims) (pgtype.UUID, error) {
	// Honour the token's own clusters claim, when it carries one: mode 2 has
	// no Shepherd-side binding to scope the token, so this claim is the only
	// cluster allowlist. Empty means "any cluster in the asserted org".
	if len(claims.Clusters) > 0 && !slices.Contains(claims.Clusters, cluster) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is not permitted by this token's clusters claim", cluster))
	}
	org, err := s.store.Queries.GetOrgByName(ctx, claims.Org)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("token asserts organisation %q, which does not exist", claims.Org))
	}
	if err != nil {
		return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving asserted org: %w", err))
	}
	claimed, err := s.store.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{
		ID:    coll.ClusterID,
		OrgID: org.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("cluster %q is claimed by another organisation", cluster))
	}
	if err != nil {
		return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("claiming cluster: %w", err))
	}
	return claimed, nil
}

// allowlistPermits reports whether value is allowed by a jsonb string-array
// allowlist. An empty (or absent) array means "any" — the deliberate default
// so a binding without a cluster/role list is unrestricted within its org.
func allowlistPermits(raw json.RawMessage, value string) bool {
	if len(raw) == 0 {
		return true
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return true
	}
	return slices.Contains(list, value)
}

func effectiveAttrs(local, deprecated map[string]string) map[string]string {
	if len(local) > 0 {
		return local
	}
	return deprecated
}

// requireClusterRole extracts and validates cluster+role from attributes.
func requireClusterRole(attrs map[string]string) (cluster, role string, err error) {
	cluster = attrs["cluster"]
	role = attrs["role"]
	var missing []string
	if cluster == "" {
		missing = append(missing, "cluster")
	}
	if role == "" {
		missing = append(missing, "role")
	} else if signals.ValidateRole(role) != nil {
		// Collectors keep this message shape; the role set is signals.Roles(),
		// the same table mgmtapi checks binding allowlists against.
		return "", "", fmt.Errorf("role %q must be one of: %s", role, strings.Join(signals.Roles(), "|"))
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("missing required attributes: %s", strings.Join(missing, ", "))
	}
	return cluster, role, nil
}

// recomputeServeCache assembles and validates the merged config for a single collector.
// It returns (content, hash, error). On error the caller should serve the previous cached value.
//
// reqAttrs is GetConfig's caller-supplied attrs (effectiveAttrs(req.Msg...)) —
// PR-8b's gated freshness requirement: local_attributes matching on this hot
// path must use the CURRENT request's self-reported attributes, never a
// re-query of collector_instances, which would still reflect the previous
// heartbeat (this function's own upsertCollectorInstance write for the
// current one hasn't necessarily landed/committed before this read would
// run). One known, accepted edge: this recompute runs inside GetConfig's
// singleflight.Do keyed by collector ID (§200), so two nearly-simultaneous
// polls for the SAME collector reporting DIFFERENT local_attributes can have
// the "losing" caller's own reqAttrs discarded in favor of whichever request
// actually executed the closure — same class of narrow, accepted relaxation
// as PR-7's "latest instance wins" multi-instance simplification (LABEL-
// MATCHING-PLAN.md §5), not something this change introduces new risk of.
func (s *Service) recomputeServeCache(ctx context.Context, coll sqlc.Collector, orgID pgtype.UUID, reqAttrs map[string]string) (string, string, error) {
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("listing pipelines: %w", err)
	}

	// Admin labels and local_attributes only participate in matching once the
	// org has opted into each independently (procoduck/shepherd#139) — an org
	// with both flags off must reproduce exactly the pre-#139 {cluster,
	// role}-only behavior, byte for byte.
	var adminLabels, localAttrs map[string]string
	if org, orgErr := s.store.Queries.GetOrgByID(ctx, orgID); orgErr == nil {
		if org.AllowLabelMatching {
			if jsonErr := json.Unmarshal(coll.Labels, &adminLabels); jsonErr != nil {
				s.logger.Warn("recomputeServeCache: decoding collector labels", "collector_id", coll.ID.String(), "err", jsonErr)
				adminLabels = nil
			}
		}
		if org.AllowLocalAttributeMatching {
			localAttrs = reqAttrs
		}
	}

	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		// Git-sourced pipelines are matched by the collector their repo link targets,
		// not by matchers; leaving this empty makes them match nothing.
		repoLinkCollectorID := ""
		if ep.RepoLinkCollectorID.Valid {
			repoLinkCollectorID = ep.RepoLinkCollectorID.String()
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID:                  ep.ID.String(),
			Name:                ep.Name,
			Contents:            ep.Contents,
			Matchers:            m,
			Source:              ep.Source,
			RepoLinkCollectorID: repoLinkCollectorID,
		})
	}

	cluster, clusterErr := s.store.Queries.GetClusterByID(ctx, coll.ClusterID)
	clusterName := ""
	if clusterErr == nil {
		clusterName = cluster.Name
	}

	if s.validator == nil {
		// Stage 1 is enforced unconditionally by serve.ComputeServed below
		// regardless of whether a validator is wired — this used to gate
		// whether Stage 1 ran at all (see git history), which meant a merged
		// config that failed to even parse still reached serve_cache with
		// no validator configured. This line exists only so a misconfigured
		// deployment is discoverable in logs, not to change behavior.
		s.logger.Debug("recomputeServeCache: no validator configured; merged output is still Stage-1 validated", "collector_id", coll.ID.String())
	}

	// internal/serve.ComputeServed is the single merge -> append-baseline ->
	// hash -> Stage-1-validate implementation this lazy path shares with
	// internal/mgmtapi's eager recompute (docs/gateway-tier-plan.md §10).
	result, err := serve.ComputeServed(ctx,
		serve.Deps{Schema: s.schema, BeaconBaseline: s.beaconBaseline},
		serve.Collector{ID: coll.ID.String(), Cluster: clusterName, Role: coll.Role, AdminLabels: adminLabels, LocalAttrs: localAttrs},
		mergePipelines,
	)
	if err != nil {
		return "", "", err
	}
	if result.BaselineErr != nil {
		// Render failure is a static-config bug (bad Label, e.g.), not a
		// per-collector condition — serve.ComputeServed already degraded to
		// serving without the baseline; say so loudly.
		s.logger.Error("appending beacon baseline pipeline failed; serving without it", "collector_id", coll.ID.String(), "err", result.BaselineErr)
	}

	return result.Content, result.Hash, nil
}
