package mgmtapi

import (
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/telemetry"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// Router builds the /api chi sub-router. It serves only what the
// shepherd.mgmt.v1 Connect contract deliberately leaves out (see
// docs/archive/api-contract-design.md, "Out of contract, unchanged"): the
// Alloy component schema artifacts, a large dynamic JSON document with ETag
// caching. The legacy REST shim that used to live here — plain-JSON routes
// duplicating Connect procedures — was deprecated in v0.9.0 and removed in
// v0.11.0; machine callers use Connect. /api/version and /api/auth/* are
// registered by internal/server, not here.
func Router() http.Handler {
	// Schema registry: embedded artifacts + overlay. If the registry fails to
	// initialize (corrupt embed), the schema handler answers 503 per request.
	schemaReg, _ := schema.New(schema.Embedded, version.AlloySchemaVersion) //nolint:errcheck // a nil registry is handled per request by SchemaHandler
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

	// Schema endpoints — accessible to any authenticated user (reader role).
	// GET /api/schema/current → pinned fleet version merged with overlay.
	// GET /api/schema/{version} → specific version merged with overlay (ETag cached).
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/schema/current", schemaHandler.GetCurrent)
		r.Get("/schema/{version}", schemaHandler.Get)
	})

	return r
}

// MountOption configures optional MountRPC dependencies — things the route
// tree can supply but a bare RPC-surface test wiring cannot. Variadic so the
// several tests that call MountRPC with the original five arguments keep
// compiling.
type MountOption func(*mountConfig)

type mountConfig struct {
	oidc *auth.Handler
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
// alongside r.Mount("/api", Router()).
func MountRPC(r chi.Router, st *store.Store, cfg *config.Config, enc *crypto.Encryptor, logger *slog.Logger, opts ...MountOption) {
	var mc mountConfig
	for _, opt := range opts {
		opt(&mc)
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "mgmtapi.rpc")
	v := validate.New(&cfg.Validate)
	schemaReg, schemaErr := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if schemaErr != nil {
		// schemaReg is nil here. The services that dereference the registry
		// must nil-check it; UpgradeCheck answers CodeUnavailable (see
		// errSchemaUnavailable in rpc_visual.go), Render/Validate keep their
		// legacy CodeInvalidArgument mapping, and GraphView degrades to a
		// schemaless parse. Everything else keeps working, so mounting
		// proceeds — but a corrupt embedded schema is a build defect worth
		// shouting about, not a per-request curiosity.
		logger.Error("schema registry unavailable; schema-dependent RPCs will answer unavailable", "err", schemaErr)
	}

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

	mounts := []func() (string, http.Handler){
		func() (string, http.Handler) {
			return mgmtv1connect.NewMeServiceHandler(NewMeService(st, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewAdminServiceHandler(NewAdminService(st, logger, WithOIDCHandler(mc.oidc)), authz...)
		},
		func() (string, http.Handler) {
			var users *auth.UserStore
			if mc.oidc != nil {
				users = mc.oidc.Users()
			}
			return mgmtv1connect.NewUserServiceHandler(NewUserService(st, users, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewFleetServiceHandler(NewFleetService(st, logger, WithFleetSchema(schemaReg)), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewPipelineServiceHandler(NewPipelineService(st, v, schemaReg, logger, WithBeaconRemoteWrite(cfg.Server.BaseURL, beacon.OAuth2ForBeacon(cfg.OIDC.BeaconAuth, cfg.OIDC.AgentTokenURL, cfg.OIDC.AgentScopes))), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewDestinationServiceHandler(NewDestinationService(st, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewGitOpsServiceHandler(NewGitOpsService(st, enc, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewWizardServiceHandler(NewWizardService(st, v, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewVisualServiceHandler(NewVisualService(st, v, schemaReg, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewSimulateServiceHandler(NewSimulateService(st, cfg.Simulator, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewAuditServiceHandler(NewAuditService(st, logger), authz...)
		},
		func() (string, http.Handler) {
			return mgmtv1connect.NewTenantRouteServiceHandler(NewTenantRouteService(st, logger, WithGatewayPublicBaseURL(cfg.Gateway.Routes.PublicBaseURL)), authz...)
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
