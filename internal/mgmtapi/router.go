package mgmtapi

import (
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/schema"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/telemetry"
	"shepherd/internal/validate"
)

// Router builds the /api chi sub-router. It serves only what the
// shepherd.mgmt.v1 Connect contract deliberately leaves out (see
// docs/archive/api-contract-design.md, "Out of contract, unchanged"): the
// Alloy component schema artifacts, a large dynamic JSON document with ETag
// caching. The legacy REST shim that used to live here — plain-JSON routes
// duplicating Connect procedures — was deprecated in v0.9.0 and removed in
// v0.11.0; machine callers use Connect. /api/version and /api/auth/* are
// registered by internal/server, not here.
//
// schemaReg is the registry internal/server loaded once at boot (it refuses to
// start without one).
func Router(schemaReg *schema.Registry) http.Handler {
	schemaHandler := NewSchemaHandler(schemaReg)

	r := chi.NewRouter()

	// NotFound: unmatched /api/* paths — including every removed shim route —
	// return 404 JSON instead of the default chi response.
	apiNotFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`)) //nolint:errcheck
	})
	r.NotFound(apiNotFound)
	r.MethodNotAllowed(apiNotFound)

	// Schema endpoints — accessible to any authenticated user (viewer role).
	// GET /api/schema/current → pinned fleet version merged with overlay.
	// GET /api/schema/{version} → specific version merged with overlay (ETag cached).
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		MountSchemaRoutes(r, schemaHandler)
	})

	return r
}

// MountSchemaRoutes registers the schema artifact routes on r, for GET and
// HEAD: a HEAD (a cheap "is it there, which ETag" probe) used to fall through
// to the 404 handler. net/http drops the body a HEAD handler writes, so the
// GET handlers serve both.
func MountSchemaRoutes(r chi.Router, h *SchemaHandler) {
	r.Get("/schema/current", h.GetCurrent)
	r.Head("/schema/current", h.GetCurrent)
	r.Get("/schema/{version}", h.Get)
	r.Head("/schema/{version}", h.Get)
}

// MountOption configures optional MountRPC dependencies — things the route
// tree can supply but a bare RPC-surface test wiring cannot. Variadic so the
// several tests that call MountRPC with the original five arguments keep
// compiling.
type MountOption func(*mountConfig)

type mountConfig struct {
	oidc         *auth.Handler
	routeApplyOn func() bool
	tokenChanged func(tokenID string)
	// identityChanged: see WithAgentIdentityChanged.
	identityChanged func(issuer, appID string)
}

// WithAgentIdentityChanged supplies what AdminService.CreateAgentIdentity
// and DeleteAgentIdentity call after their change commits — internal/server
// passes agentapi.Service.ForgetAgentIdentity, so a created, deleted or
// moved identity is never answered from the outage cache under its old
// binding on this replica (#385).
func WithAgentIdentityChanged(f func(issuer, appID string)) MountOption {
	return func(m *mountConfig) { m.identityChanged = f }
}

// WithAgentTokenChanged supplies what AdminService.RevokeAgentToken and
// BindAgentToken call after their change commits — internal/server passes
// the collector gate's agentapi.TokenVerdicts.Evict, so a revoked or rebound
// token stops verifying from the outage cache on this replica immediately
// (#374).
func WithAgentTokenChanged(f func(tokenID string)) MountOption {
	return func(m *mountConfig) { m.tokenChanged = f }
}

// WithRouteApplyRunning supplies whether the tenant-route reconciler is
// running in this process (internal/server sets it once the reconciler has
// started). Without it, TenantRouteService assumes it runs exactly when
// gateway.routes.apply.enabled is set.
func WithRouteApplyRunning(running func() bool) MountOption {
	return func(m *mountConfig) { m.routeApplyOn = running }
}

// WithOIDCSettings supplies the live auth handler that AdminService's OIDC
// settings procedures read and write through. Without it those procedures
// answer CodeUnavailable; the production route tree always supplies it (see
// internal/server/server.go).
func WithOIDCSettings(h *auth.Handler) MountOption {
	return func(m *mountConfig) { m.oidc = h }
}

// MountRPC mounts every shepherd.mgmt.v1 Connect service handler onto r,
// each wrapped with the shared authz interceptor (rpc_interceptor.go). Call
// this inside the same chi router group that already applies session +
// CSRF middleware — see internal/server/server.go, where it is called
// alongside r.Mount("/api", Router(schemaReg)).
//
// schemaReg is the component schema registry internal/server loaded once at
// boot; every service here shares it. It must be non-nil: role enforcement on
// the eager serve path and Stage 3 refuse to run without it, and the server
// refuses to start rather than mount a degraded API.
func MountRPC(r chi.Router, st *store.Store, cfg *config.Config, enc *crypto.Encryptor, schemaReg *schema.Registry, logger *slog.Logger, opts ...MountOption) {
	if schemaReg == nil {
		panic("mgmtapi.MountRPC: nil schema registry")
	}
	var mc mountConfig
	for _, opt := range opts {
		opt(&mc)
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "mgmtapi.rpc")
	v := validate.New(&cfg.Validate)

	// Order matters: the service-account auth gate runs before any
	// interceptor — on the headers alone, before the body is decoded — so a
	// machine caller's identity (if any) is in ctx before
	// newAuthzInterceptor's role/org decision runs — see
	// authorizeProcedure's service-account branch (rpc_interceptor.go). A
	// human-session request (no Basic-auth Authorization header) passes
	// through the gate unchanged.
	// telemetry.Interceptor is the outermost interceptor so RPC latency
	// covers the authz work and a PermissionDenied is counted rather than
	// invisible; a call the gate refuses never reaches it, which is why the
	// gate is wrapped by telemetry.RequestGate.
	saLimiter := newSARateLimiter(cfg.Auth.ServiceAccountRateLimit, cfg.Auth.ServiceAccountRateBurst)
	authz := []connect.HandlerOption{
		connect.WithRequestGate(telemetry.RequestGate(newServiceAccountAuthGate(st, saLimiter))),
		connect.WithInterceptors(telemetry.Interceptor(), newAuthzInterceptor(st)),
	}

	routeApplyOn := mc.routeApplyOn
	if routeApplyOn == nil {
		enabled := cfg.Gateway.Routes.Apply.Enabled
		routeApplyOn = func() bool { return enabled }
	}

	requireOwned := cfg.Tenants.RequireOwned
	pipelineSvc := NewPipelineService(st, v, schemaReg, logger, WithBeaconBaseline(serve.BeaconBaseline(cfg)), WithRequireOwnedTenants(requireOwned))

	mounts := []func() (string, http.Handler){
		func() (string, http.Handler) {
			return mgmtv1connect.NewMeServiceHandler(NewMeService(st, logger, WithMeRequireOwnedTenants(requireOwned)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewAdminServiceHandler(NewAdminService(st, logger, WithOIDCHandler(mc.oidc), WithTokenChangedHook(mc.tokenChanged), WithIdentityChangedHook(mc.identityChanged), WithAdminRequireOwnedTenants(requireOwned)), authz...)
		},
		func() (string, http.Handler) {
			var users *auth.UserStore
			if mc.oidc != nil {
				users = mc.oidc.Users()
			}
			return mgmtv1connect.NewUserServiceHandler(NewUserService(st, users, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewFleetServiceHandler(NewFleetService(st, logger, WithFleetSchema(schemaReg), WithFleetBaseURL(cfg.Server.BaseURL), WithFleetInactiveAfter(cfg.Agent.InactiveAfter)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewPipelineServiceHandler(pipelineSvc, authz...)
		},
		func() (string, http.Handler) {
			// The same PipelineService instance: a destination update
			// re-renders its wizard pipelines through that service's gate
			// and eager recompute (#262).
			return mgmtv1connect.NewDestinationServiceHandler(NewDestinationService(st, pipelineSvc, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewGitOpsServiceHandler(NewGitOpsService(st, enc, pipelineSvc, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewWizardServiceHandler(NewWizardService(st, v, logger, WithWizardSchema(schemaReg), WithWizardRequireOwnedTenants(requireOwned)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewVisualServiceHandler(NewVisualService(st, v, schemaReg, logger, WithVisualRequireOwnedTenants(requireOwned)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewSimulateServiceHandler(NewSimulateService(st, cfg.Simulator, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewAuditServiceHandler(NewAuditService(st, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewTenantRouteServiceHandler(NewTenantRouteService(st, logger, WithGatewayPublicBaseURL(cfg.Gateway.Routes.PublicBaseURL), WithTenantRouteApplyRunning(routeApplyOn)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewTeamServiceHandler(NewTeamService(st, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewServiceAccountServiceHandler(NewServiceAccountService(st, logger), authz...)
		},
	}
	for _, mount := range mounts {
		path, handler := mount()
		r.Mount(path, handler)
	}
}
