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
	// WithBeaconBaseline. Zero value (RemoteWriteURL == "") means
	// disabled; beacon.AppendBaseline treats that as "leave content
	// unchanged", never as an error.
	beaconBaseline beacon.BaselineConfig
	// afterRecompute, when set, runs between the lazy recompute and its
	// conditional write — test-only (export_test.go), so a spec can land a
	// mark exactly mid-recompute. Nil in production.
	afterRecompute func(context.Context)
	// refusals rate-limits the Warn log of identity refusals.
	refusals *refusalLog
	// lastServed is the outage fallback (WithOutageCache); nil = disabled.
	lastServed *lastServed
}

// ServiceOption configures optional Service behavior not required by every
// caller (server.go's production wiring vs. a test constructing a minimal
// Service) — the same variadic-option shape internal/merge.AssembleOption
// already uses for the same reason (WithRoleEnforcement).
type ServiceOption func(*Service)

// WithBeaconBaseline sets D6's baseline pipeline on the lazy recompute path:
// every claimed collector's served config gets beacon.RenderBaselinePipeline
// appended. Production passes serve.BeaconBaseline(cfg) — the same value the
// eager paths get, so the two can never disagree (#369). A baseline with an
// empty RemoteWriteURL is equivalent to not applying this option at all
// (beacon.AppendBaseline's documented no-op).
func WithBeaconBaseline(b beacon.BaselineConfig) ServiceOption {
	return func(s *Service) {
		s.beaconBaseline = b
	}
}

// WithOutageCache enables the last-served fallback (outage.go): every
// successful GetConfig remembers what it served for (principal, cluster,
// role) for ttl, and a GetConfig or RegisterCollector that cannot reach the
// database answers from it instead of failing. ttl <= 0 leaves it disabled.
func WithOutageCache(ttl time.Duration) ServiceOption {
	return func(s *Service) {
		s.lastServed = newLastServed(ttl, lastServedMaxEntries, lastServedMaxBytes)
	}
}

// New creates a new agent API Service. reg is the component schema registry
// role enforcement runs against; it must be non-nil. internal/server loads it
// once at boot and refuses to start without it, so a nil reg is a programming
// error: the lazy recompute then fails (serve.ComputeServed refuses) and the
// collector keeps its previous config, never an unenforced one.
func New(st *store.Store, v *validate.Validator, logger *slog.Logger, reg *schema.Registry, opts ...ServiceOption) *Service {
	s := &Service{
		store: st, validator: v, logger: logger.With("component", "agentapi"), schema: reg,
		refusals: newRefusalLog(refusalLogWindow, refusalLogMax),
	}
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
	if err := requireInstanceID(req.Msg.Id); err != nil {
		return nil, err
	}
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	key := servedKey(PrincipalFrom(ctx), cluster, role)
	dbCtx, cancel := serverCtx(ctx)
	defer cancel()
	if err := s.register(dbCtx, req.Msg, attrs, cluster, role); err != nil {
		if gone := callerGone(ctx); gone != nil {
			return nil, gone
		}
		if !isOutage(err) {
			s.lastServed.forget(key)
			return nil, err
		}
		// The database is unreachable (#374). Alloy exits when its start-up
		// RegisterCollector fails, so a collector this replica served within
		// agent.outage_cache_ttl is acknowledged without writing anything:
		// GetConfig self-registers it (§4.1) on the first poll after the
		// database is back. Anyone else gets Unavailable.
		if _, ok := s.lastServed.get(key); ok {
			s.logger.Warn("database unavailable; acknowledging registration of a recently served collector without recording it",
				"instance_id", truncateID(req.Msg.Id), "cluster", cluster, "role", role, "err", err)
			return connect.NewResponse(&collectorv1.RegisterCollectorResponse{}), nil
		}
		s.logger.Warn("database unavailable", "rpc", "RegisterCollector",
			"instance_id", truncateID(req.Msg.Id), "cluster", cluster, "role", role, "err", err)
		return nil, unavailable()
	}

	return connect.NewResponse(&collectorv1.RegisterCollectorResponse{}), nil
}

// register is RegisterCollector's database work.
func (s *Service) register(ctx context.Context, msg *collectorv1.RegisterCollectorRequest, attrs map[string]string, cluster, role string) error {
	// Alloy's collector.register RPC sends no name field, so fall back to
	// the wire id — matching GetConfig's fallback (§4.1) — rather than
	// storing an empty string that nothing else ever heals.
	name := msg.Name
	if name == "" {
		name = msg.Id
	}
	// Same order as GetConfig: cluster + collector rows, then the org check,
	// then the instance row — a refused registration leaves no instance trace.
	coll, err := s.upsertCollector(ctx, cluster, role)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if _, err := s.resolveOrg(ctx, cluster, role, coll); err != nil {
		return s.refused(err, "RegisterCollector", msg.Id, coll.ID, cluster, role)
	}
	if err := s.bindInstance(ctx, msg.Id, name, coll, attrs); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
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
	if err := requireInstanceID(req.Msg.Id); err != nil {
		return nil, err
	}
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	key := servedKey(PrincipalFrom(ctx), cluster, role)
	dbCtx, cancel := serverCtx(ctx)
	defer cancel()
	served, err := s.resolveServed(dbCtx, req.Msg, attrs, cluster, role)
	if gone := callerGone(ctx); err != nil && gone != nil {
		return nil, gone
	}
	switch {
	case err == nil:
		s.lastServed.put(key, served)
	case !isOutage(err):
		// Refused, or failed for a reason other than an outage: what was
		// served before must not be re-served from memory either.
		s.lastServed.forget(key)
		return nil, err
	default:
		// The database is unreachable (#374): answer from the last config
		// this replica served this principal for this collector, with the
		// same hash semantics, or Unavailable. Nothing is written.
		cached, ok := s.lastServed.get(key)
		if !ok {
			s.logger.Warn("database unavailable", "rpc", "GetConfig",
				"instance_id", truncateID(req.Msg.Id), "cluster", cluster, "role", role, "err", err)
			return nil, unavailable()
		}
		s.logger.Warn("database unavailable; serving the last config this replica served",
			"instance_id", truncateID(req.Msg.Id), "cluster", cluster, "role", role, "hash", cached.hash, "err", err)
		served = cached
	}

	if req.Msg.Hash == served.hash {
		metrics.ObserveGetConfig("not_modified", start)
		return connect.NewResponse(&collectorv1.GetConfigResponse{
			NotModified: true,
			Hash:        served.hash,
		}), nil
	}
	metrics.ObserveGetConfig("served", start)
	return connect.NewResponse(&collectorv1.GetConfigResponse{
		Content:     served.content,
		Hash:        served.hash,
		NotModified: false,
	}), nil
}

// resolveServed is GetConfig's database work: self-registration, the org
// check, status bookkeeping and the serve-cache read (recomputing when
// dirty). It returns the config to serve; GetConfig turns it into the
// response and remembers it for an outage.
func (s *Service) resolveServed(
	ctx context.Context,
	msg *collectorv1.GetConfigRequest,
	attrs map[string]string,
	cluster, role string,
) (servedConfig, error) {
	// Upsert instance (self-register on GetConfig too — §4.1). GetConfigRequest
	// carries no dedicated name field, so prefer a name reported via attributes;
	// otherwise fall back to the wire id, matching first-registration behavior.
	// Either way, the SQL upsert preserves any existing non-wire-id display
	// name rather than clobbering it with the wire id on every subsequent poll.
	name := attrs["collector.name"]
	if name == "" {
		name = msg.Id
	}

	// Cluster + collector rows first: unclaimed-cluster discovery and OIDC
	// auto-claim (resolveOrg) both need them. The instance row, its status
	// and its local_attributes are written only after the org check passes,
	// so a refused poll leaves no instance-row trace (R9/R10).
	coll, err := s.upsertCollector(ctx, cluster, role)
	if err != nil {
		return servedConfig{}, connect.NewError(connect.CodeInternal, err)
	}

	orgID, err := s.resolveOrg(ctx, cluster, role, coll)
	if err != nil {
		return servedConfig{}, s.refused(err, "GetConfig", msg.Id, coll.ID, cluster, role)
	}

	// PR-9's match-drift short-circuit needs the instance's PREVIOUS
	// local_attributes before bindInstance overwrites it. This read runs on
	// every poll, so it must stay cheap: one extra SELECT, never a pipeline
	// list + diff, for the overwhelmingly common case of unchanged
	// attributes — see emitLocalAttrsMatchDrift's doc comment for why the
	// comparison itself is over decoded maps, not raw jsonb bytes.
	var beforeAttrsJSON json.RawMessage
	// Instances are keyed per collector (0036), so this is the row this
	// poll is about to update — a first poll under a new collector has none.
	if prev, prevErr := s.store.Queries.GetCollectorInstance(ctx, sqlc.GetCollectorInstanceParams{
		CollectorID: coll.ID, ID: msg.Id,
	}); prevErr == nil {
		beforeAttrsJSON = prev.LocalAttributes
	}

	if err := s.bindInstance(ctx, msg.Id, name, coll, attrs); err != nil {
		return servedConfig{}, connect.NewError(connect.CodeInternal, err)
	}

	// Persist remote_config_status if provided.
	if msg.RemoteConfigStatus != nil {
		statusStr := strings.TrimPrefix(msg.RemoteConfigStatus.Status.String(), "RemoteConfigStatuses_")
		errMsg := msg.RemoteConfigStatus.ErrorMessage
		if err := s.store.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
			CollectorID:        coll.ID,
			ID:                 msg.Id,
			RemoteConfigStatus: pgtype.Text{String: statusStr, Valid: true},
			RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
			// Which config this status is about (#115): the hash sent with it.
			StatusHash: pgtype.Text{String: msg.Hash, Valid: msg.Hash != ""},
		}); err != nil {
			s.logger.Warn("failed to update instance status", "instance_id", msg.Id, "err", err)
		}
	}

	// Unclaimed cluster: serve empty config.
	if !orgID.Valid {
		s.applySilentPoll(ctx, coll.ID, msg, emptyHash)
		return servedConfig{content: "", hash: emptyHash}, nil
	}

	s.emitLocalAttrsMatchDrift(ctx, orgID, coll, cluster, role, beforeAttrsJSON, attrs)

	// Claimed: read from serve cache; recompute if dirty, or if the row was
	// computed in an older serve format (serve.Format, migration 0033) — a
	// clean row an older binary wrote is as stale as a dirty one.
	cache, err := s.store.Queries.GetServeCache(ctx, coll.ID)
	if isOutage(err) {
		// Not "no row yet": recomputing cannot reach the database either,
		// and falling through would serve the zero row's empty content.
		return servedConfig{}, fmt.Errorf("reading serve cache: %w", err)
	}
	if err != nil || cache.Dirty || !serve.Current(cache.Format) {
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
			served, err := s.recomputeServeCache(ctx, coll, orgID, attrs)
			if err != nil {
				return nil, err
			}
			if s.afterRecompute != nil {
				s.afterRecompute(ctx)
			}
			updated, written, err := serve.Store(ctx, s.store.Queries, coll.ID, expectedSeq, served)
			if err != nil {
				return nil, err
			}
			if written {
				return updated, nil
			}
			// Superseded: a newer mark arrived while this recompute ran,
			// or the eager recompute already served this generation.
			// Serve whatever the row holds now — if it is still dirty,
			// the next poll recomputes against the newer generation.
			current, getErr := s.store.Queries.GetServeCache(ctx, coll.ID)
			if getErr != nil {
				return nil, fmt.Errorf("re-reading superseded serve cache: %w", getErr)
			}
			if !serve.Current(current.Format) {
				// Except a row in an older format (a mark reset it, and
				// an older binary may since have written old-format
				// bytes into it): never serve those as current. Serve
				// this recompute's result instead, without caching it;
				// the next poll recomputes against the newer generation
				// and writes. Usually that result is at least as fresh
				// as the row. The accepted exception is mid-rollout: an
				// older binary can recompute the newer generation (N+1)
				// and write it clean at format 0 while this serves its
				// generation-N result, so for this one poll the
				// collector may get older pipeline content than the row
				// holds. The row stays format 0, so the next poll
				// recomputes and serves N+1 — it self-heals.
				return sqlc.ServeCache{CollectorID: coll.ID, Content: served.Content, Hash: served.Hash, Dirty: true, Format: serve.Format}, nil
			}
			return current, nil
		})
		if recomputeErr != nil && err != nil && isOutage(recomputeErr) {
			// No row was read and the database went away mid-recompute:
			// an outage, not "serve empty" (#374).
			return servedConfig{}, fmt.Errorf("recomputing served config: %w", recomputeErr)
		}
		if recomputeErr != nil {
			// Fall through: serve whatever is in cache (or empty if no cache
			// yet) — RecordFailure logs, counts and audits the failure.
			serve.RecordFailure(ctx, s.store, s.logger, serve.Failure{
				CollectorID: coll.ID, OrgID: orgID, DirtySeq: expectedSeq, Path: serve.PathLazy, Err: recomputeErr,
			})
		} else {
			cache = result.(sqlc.ServeCache)
		}
	}

	// A poll with no fresh status but the hash we served: see applySilentPoll.
	s.applySilentPoll(ctx, coll.ID, msg, cache.Hash)

	return servedConfig{content: cache.Content, hash: cache.Hash}, nil
}

// applySilentPoll records what a status-less poll for the served hash means.
// Alloy re-sends a status only when its (status, error) pair changes, so the
// silence is "same outcome as last reported" — for a new config too (#115,
// F1: a new config failing with an identical error is never re-reported). A
// poll carrying effective_config is the exception that proves a load: Alloy
// sets it only after loading successfully. The SQL (ApplySilentPoll) holds
// the full rule; a status carried by the request was already persisted above
// and always wins, so this is a no-op then.
func (s *Service) applySilentPoll(ctx context.Context, collectorID pgtype.UUID, req *collectorv1.GetConfigRequest, servedHash string) {
	if req.GetRemoteConfigStatus() != nil || req.GetHash() != servedHash {
		return
	}
	loaded := len(req.GetEffectiveConfig().GetConfigMap().GetConfigMap()) > 0
	if err := s.store.Queries.ApplySilentPoll(ctx, sqlc.ApplySilentPollParams{
		CollectorID: collectorID,
		ID:          req.GetId(),
		Loaded:      loaded,
		PolledHash:  pgtype.Text{String: req.GetHash(), Valid: req.GetHash() != ""},
	}); err != nil {
		s.logger.Warn("failed to record silent poll", "instance_id", req.GetId(), "err", err)
	}
}

// UnregisterCollector marks the instance rows with the request's wire id
// that the caller may act for (R9). Instances are keyed per collector
// (0036), so one id can have a row under several collectors; the request
// carries the id alone, so the caller's scope comes from its principal
// (callerScope):
//
//   - an OIDC principal with an org of its own (a binding, or a mode-2
//     asserted org) marks only rows whose collector's cluster is claimed to
//     that org and, for a binding, inside its cluster/role allowlists (for an
//     asserted org, inside the token's clusters claim);
//   - an agent token marks only rows inside its own binding (org and
//     allowlists), the same scope as an OIDC binding; an unbound token is
//     refused (token_unbound);
//   - an OIDC principal with no org of its own marks every row with the id.
//     Its org is resolved per request from the cluster claim of the
//     collector it acts as, so it can already poll as any of them: this
//     grants nothing new.
//
// The answer is the same success whether rows were marked, the id is unknown,
// or every row is out of scope, so the call is no existence oracle across
// orgs; out-of-scope rows are only counted (unregister_denied) and logged.
func (s *Service) UnregisterCollector(
	ctx context.Context,
	req *connect.Request[collectorv1.UnregisterCollectorRequest],
) (*connect.Response[collectorv1.UnregisterCollectorResponse], error) {
	if err := requireInstanceID(req.Msg.Id); err != nil {
		return nil, err
	}
	ok := connect.NewResponse(&collectorv1.UnregisterCollectorResponse{})
	ctx, cancel := serverCtx(ctx)
	defer cancel()
	scope, err := s.callerScope(ctx)
	var r *refusal
	if errors.As(err, &r) {
		// An unbound token: refused outright, like its other RPCs.
		return nil, s.refused(err, "UnregisterCollector", req.Msg.Id, pgtype.UUID{}, "", "")
	}
	if errors.Is(err, errUnknownAssertedOrg) {
		// The caller's scope is empty: nothing is in it.
		s.refusals.record(s.logger, "UnregisterCollector", metrics.AgentRefusedUnregisterDenied, req.Msg.Id)
		return ok, nil
	}
	if isOutage(err) {
		return nil, unavailable()
	}
	if err != nil {
		return nil, err
	}
	res, err := s.store.Queries.UnregisterInstanceScoped(ctx, sqlc.UnregisterInstanceScopedParams{
		ID:       req.Msg.Id,
		OrgID:    scope.orgID,
		Clusters: scope.clusters,
		Roles:    scope.roles,
	})
	if isOutage(err) {
		return nil, unavailable()
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unregistering instance: %w", err))
	}
	if res.OutOfScope > 0 {
		s.refusals.record(s.logger, "UnregisterCollector", metrics.AgentRefusedUnregisterDenied, req.Msg.Id)
	}
	return ok, nil
}

// errUnknownAssertedOrg: a mode-2 token asserts an org that does not exist.
var errUnknownAssertedOrg = errors.New("token asserts an organisation that does not exist")

// identityScope is what a principal may act for on its own, independent of
// any cluster it names: the org of its binding or asserted claim, and the
// allowlists that come with it (nil = any). An invalid orgID is a principal
// whose org is resolved per request from the cluster claim (OIDC with no
// binding and no asserted org) — unscoped.
type identityScope struct {
	orgID    pgtype.UUID
	clusters []string
	roles    []string
}

// callerScope resolves the principal's own org with resolveOrg's precedence:
// an agent token's binding (refused when unbound); for OIDC a binding first,
// then a mode-2 asserted org, else none.
func (s *Service) callerScope(ctx context.Context) (identityScope, error) {
	p := PrincipalFrom(ctx)
	if p.Kind == AuthKindToken {
		if !p.Binding.OrgID.Valid {
			return identityScope{}, refuse(metrics.AgentRefusedTokenUnbound, errTokenUnbound)
		}
		return identityScope{orgID: p.Binding.OrgID, clusters: allowlist(p.Binding.Clusters), roles: allowlist(p.Binding.Roles)}, nil
	}
	if p.Claims == nil {
		return identityScope{}, nil
	}
	binding, err := s.store.Queries.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{
		Issuer: p.Claims.Issuer,
		AppID:  p.Claims.AppID,
	})
	switch {
	case err == nil:
		return identityScope{orgID: binding.OrgID, clusters: allowlist(binding.Clusters), roles: allowlist(binding.Roles)}, nil
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return identityScope{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving agent identity: %w", err))
	}
	if p.Claims.Org == "" {
		return identityScope{}, nil
	}
	org, err := s.store.Queries.GetOrgByName(ctx, p.Claims.Org)
	if errors.Is(err, pgx.ErrNoRows) {
		return identityScope{}, errUnknownAssertedOrg
	}
	if err != nil {
		return identityScope{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving asserted org: %w", err))
	}
	var clusters []string
	if len(p.Claims.Clusters) > 0 {
		clusters = p.Claims.Clusters
	}
	return identityScope{orgID: org.ID, clusters: clusters}, nil
}

// allowlist decodes a jsonb string-array allowlist with allowlistPermits'
// semantics: absent, empty or malformed means "any", returned as nil.
func allowlist(raw json.RawMessage) []string {
	var list []string
	if len(raw) == 0 || json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return nil
	}
	return list
}

// refusal is an identity check's PermissionDenied, tagged with its
// shepherd_agent_refused_total reason. Handlers turn it into the RPC error
// through Service.refused, which counts and logs it once.
type refusal struct {
	reason string
	err    error
}

func (r *refusal) Error() string { return r.err.Error() }
func (r *refusal) Unwrap() error { return r.err }

func refuse(reason string, err error) error { return &refusal{reason: reason, err: err} }

// refused converts a handler error into the RPC's error. A refusal is
// counted, logged (rate-limited, see refusalLog) and returned as
// PermissionDenied; any other error passes through unchanged.
func (s *Service) refused(err error, rpc, instanceID string, collectorID pgtype.UUID, cluster, role string) error {
	var r *refusal
	if !errors.As(err, &r) {
		return err
	}
	s.refusals.record(s.logger, rpc, r.reason, instanceID,
		"collector_id", collectorID.String(), "cluster", cluster, "role", role)
	return connect.NewError(connect.CodePermissionDenied, r.err)
}

// upsertCollector upserts the cluster and collector rows a request names.
func (s *Service) upsertCollector(ctx context.Context, cluster, role string) (sqlc.Collector, error) {
	cl, err := s.store.Queries.UpsertCluster(ctx, cluster)
	if err != nil {
		return sqlc.Collector{}, fmt.Errorf("upserting cluster %q: %w", cluster, err)
	}
	coll, err := s.store.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{
		ClusterID: cl.ID,
		Role:      role,
	})
	if err != nil {
		return sqlc.Collector{}, fmt.Errorf("upserting collector: %w", err)
	}
	return coll, nil
}

// bindInstance writes the instance row (coll, instanceID) for a caller whose
// org check already passed. Instances are keyed per collector (0036): an
// instance never moves, so a poll under another cluster/role writes a new
// row and the old one ages out through agent.delete_after.
func (s *Service) bindInstance(
	ctx context.Context,
	instanceID, name string,
	coll sqlc.Collector,
	attrs map[string]string,
) error {
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

// errTokenUnbound refuses an agent token with no organisation binding.
var errTokenUnbound = errors.New("agent token is not bound to an organisation; bind it with BindAgentToken / `shepherd token bind`")

// resolveOrg decides which org a request's config is served for:
//
//  1. An agent-token principal: the token's own binding (migration 0038) —
//     its org, after enforcing its optional cluster/role allowlists, and
//     auto-claiming the cluster to that org (refusing a cluster already
//     claimed by another org). A token with no org is refused
//     (token_unbound).
//  2. An OIDC principal, by the collector-OIDC resolution chain
//     (docs/archive/plans/2026-09-16-agent-oidc-auth.md D1): an
//     agent_identities binding on its (issuer, app_id), resolved exactly as
//     a token's binding; else a mode-2 asserted org; else the admin
//     cluster-claim model, where org comes from the cluster's claim
//     (GetCollectorOrgID; an unclaimed cluster resolves to a null org and is
//     served empty upstream).
func (s *Service) resolveOrg(ctx context.Context, cluster, role string, coll sqlc.Collector) (pgtype.UUID, error) {
	p := PrincipalFrom(ctx)
	switch p.Kind {
	case AuthKindToken:
		if !p.Binding.OrgID.Valid {
			return pgtype.UUID{}, refuse(metrics.AgentRefusedTokenUnbound, errTokenUnbound)
		}
		return s.resolveBoundOrg(ctx, cluster, role, coll, p.Binding, "agent token")
	case AuthKindOIDC:
		if p.Claims == nil {
			break
		}
		binding, err := s.store.Queries.GetAgentIdentityByAppID(ctx, sqlc.GetAgentIdentityByAppIDParams{
			Issuer: p.Claims.Issuer,
			AppID:  p.Claims.AppID,
		})
		switch {
		case err == nil:
			return s.resolveBoundOrg(ctx, cluster, role, coll,
				Binding{OrgID: binding.OrgID, Clusters: binding.Clusters, Roles: binding.Roles, MayClaim: true}, "collector identity")
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

// resolveBoundOrg is the one resolver for a bound principal — an agent token
// or an OIDC agent_identities binding: it enforces the binding's cluster/role
// allowlists and auto-claims the cluster to the binding's org — or, for a
// binding that may not claim (a token an org admin or a service account
// minted), serves only a cluster the org has already claimed. subject names
// the principal kind in a refusal message.
func (s *Service) resolveBoundOrg(ctx context.Context, cluster, role string, coll sqlc.Collector, b Binding, subject string) (pgtype.UUID, error) {
	if !allowlistPermits(b.Clusters, cluster) {
		return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
			fmt.Errorf("cluster %q is not permitted for this %s", cluster, subject))
	}
	if !allowlistPermits(b.Roles, role) {
		return pgtype.UUID{}, refuse(metrics.AgentRefusedRoleDenied,
			fmt.Errorf("role %q is not permitted for this %s", role, subject))
	}
	if !b.MayClaim {
		// Claiming a cluster into an org is an app-admin decision: an org
		// token only confirms a claim that already exists. Otherwise an org
		// admin could mint a token, name a new cluster first and pull it —
		// and every OIDC collector on it that resolves by the claim — into
		// their org, or race an app admin's deliberate unclaim.
		current, err := s.store.Queries.GetCollectorOrgID(ctx, coll.ID)
		if err != nil {
			return pgtype.UUID{}, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving org: %w", err))
		}
		switch {
		case current == b.OrgID:
			return current, nil
		case !current.Valid:
			return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
				fmt.Errorf("cluster %q is not claimed by this organisation, and an organisation token cannot claim clusters; ask an app admin to claim it", cluster))
		default:
			return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
				fmt.Errorf("cluster %q is claimed by another organisation", cluster))
		}
	}
	// Bind the cluster to the binding's org, or confirm it is already ours.
	// A cluster claimed by a different org matches no row (ErrNoRows) — a
	// principal can never take over another org's cluster by naming it.
	claimed, err := s.store.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{
		ID:    coll.ClusterID,
		OrgID: b.OrgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
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
		return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
			fmt.Errorf("cluster %q is not permitted by this token's clusters claim", cluster))
	}
	org, err := s.store.Queries.GetOrgByName(ctx, claims.Org)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
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
		return pgtype.UUID{}, refuse(metrics.AgentRefusedClaimDenied,
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

// effectiveAttrs returns local_attributes, falling back to deprecated attributes if empty.
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
	} else if len(cluster) > MaxIdentifierBytes {
		// The cluster name becomes a row key and appears in refusal logs.
		return "", "", fmt.Errorf("cluster is %d bytes; at most %d allowed", len(cluster), MaxIdentifierBytes)
	}
	if role == "" {
		missing = append(missing, "role")
	} else if signals.ValidateRole(role) != nil {
		// Collectors keep this message shape; the role set is signals.Roles(),
		// the same table mgmtapi checks binding allowlists against.
		return "", "", fmt.Errorf("role %q must be one of: %s", truncateID(role), strings.Join(signals.Roles(), "|"))
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
// heartbeat (this function's own bindInstance write for the
// current one hasn't necessarily landed/committed before this read would
// run). One known, accepted edge: this recompute runs inside GetConfig's
// singleflight.Do keyed by collector ID (§200), so two nearly-simultaneous
// polls for the SAME collector reporting DIFFERENT local_attributes can have
// the "losing" caller's own reqAttrs discarded in favor of whichever request
// actually executed the closure — same class of narrow, accepted relaxation
// as PR-7's "latest instance wins" multi-instance simplification (LABEL-
// MATCHING-PLAN.md §5), not something this change introduces new risk of.
func (s *Service) recomputeServeCache(ctx context.Context, coll sqlc.Collector, orgID pgtype.UUID, reqAttrs map[string]string) (serve.Result, error) {
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesForMerge(ctx, orgID)
	if err != nil {
		return serve.Result{}, fmt.Errorf("listing pipelines: %w", err)
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

	// The same row -> merge.Pipeline conversion mgmtapi's eager recompute
	// uses: git pipelines carry their repo link's collector (they match by
	// it, not by matchers) and every pipeline its current revision for the
	// served header (#296) — byte parity between the two paths depends on it.
	mergePipelines := serve.PipelinesFromRows(enabledPipelines)

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
		return serve.Result{}, err
	}
	if result.BaselineErr != nil {
		// Render failure is a static-config bug (bad Label, e.g.), not a
		// per-collector condition — serve.ComputeServed already degraded to
		// serving without the baseline; say so loudly.
		s.logger.Error("appending beacon baseline pipeline failed; serving without it", "collector_id", coll.ID.String(), "err", result.BaselineErr)
	}

	return result, nil
}
