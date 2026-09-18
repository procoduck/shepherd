package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// validRoles is the set of allowed collector roles.
var validRoles = []string{"metrics", "logs", "singleton", "receiver"}

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
		s.maybeClearFailedStatus(ctx, req.Msg.Id, req.Msg.RemoteConfigStatus, req.Msg.Hash, emptyHash)
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
			newContent, newHash, err := s.recomputeServeCache(ctx, coll, orgID)
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

	// A poll reporting no fresh status but a hash matching what we actually
	// served means the agent is healthy on the current config (B1): clear a
	// stale FAILED marker. A RemoteConfigStatus persisted just above always
	// wins, including a repeated FAILED — see maybeClearFailedStatus.
	s.maybeClearFailedStatus(ctx, req.Msg.Id, req.Msg.RemoteConfigStatus, req.Msg.Hash, cache.Hash)

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

// maybeClearFailedStatus implements the B1 clearing rule: when a poll
// carries no RemoteConfigStatus payload and the agent's reported hash
// equals what GetConfig actually served, the agent is healthy on its
// current config, so a stale FAILED marker is cleared back to APPLIED.
// If the request DOES carry a RemoteConfigStatus — including a repeated
// FAILED — GetConfig has already persisted it via UpdateInstanceStatus
// above, and that write wins: status is non-nil here, so this is a no-op.
func (s *Service) maybeClearFailedStatus(
	ctx context.Context,
	instanceID string,
	status *collectorv1.RemoteConfigStatus,
	agentHash, servedHash string,
) {
	if status != nil || agentHash != servedHash {
		return
	}
	if err := s.store.Queries.ClearStaleFailedStatus(ctx, instanceID); err != nil {
		s.logger.Warn("failed to clear stale FAILED status", "instance_id", instanceID, "err", err)
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

// effectiveAttrs returns local_attributes, falling back to deprecated attributes if empty.
// resolveOrg decides which org a request's config is served for, applying the
// collector-OIDC resolution chain (docs/plans/2026-09-16-agent-oidc-auth.md D1):
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
	} else if !slices.Contains(validRoles, role) {
		return "", "", fmt.Errorf("role %q must be one of: %s", role, strings.Join(validRoles, "|"))
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("missing required attributes: %s", strings.Join(missing, ", "))
	}
	return cluster, role, nil
}

// recomputeServeCache assembles and validates the merged config for a single collector.
// It returns (content, hash, error). On error the caller should serve the previous cached value.
func (s *Service) recomputeServeCache(ctx context.Context, coll sqlc.Collector, orgID pgtype.UUID) (string, string, error) {
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("listing pipelines: %w", err)
	}

	// Admin labels only participate in matching once the org has opted in
	// (procoduck/shepherd#139) — an org with the flag off must reproduce
	// exactly the pre-#139 {cluster, role}-only behavior, byte for byte.
	var adminLabels map[string]string
	if org, orgErr := s.store.Queries.GetOrgByID(ctx, orgID); orgErr == nil && org.AllowLabelMatching {
		if jsonErr := json.Unmarshal(coll.Labels, &adminLabels); jsonErr != nil {
			s.logger.Warn("recomputeServeCache: decoding collector labels", "collector_id", coll.ID.String(), "err", jsonErr)
			adminLabels = nil
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
		serve.Collector{ID: coll.ID.String(), Cluster: clusterName, Role: coll.Role, AdminLabels: adminLabels},
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
