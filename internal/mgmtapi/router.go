package mgmtapi

import (
	"errors"
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

// requireAppOrOrgAdmin allows an app admin, or an org admin of the org named
// by the org_id query parameter, to proceed. This is the REST-shim
// equivalent of the Connect authz interceptor's reqAppOrOrgAdmin handling
// for AdminService.SearchGroups (see rpc_interceptor.go's
// authorizeProcedure) — kept in sync with it by construction since both
// call auth.Authorize with the same role constants.
func requireAppOrOrgAdmin(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := auth.SessionFromCtx(r.Context())
			if err := auth.Authorize(r.Context(), st, sess, "", auth.RoleAppAdmin); err == nil {
				next.ServeHTTP(w, r)
				return
			}
			orgID := r.URL.Query().Get("org_id")
			switch err := auth.Authorize(r.Context(), st, sess, orgID, auth.RoleOrgAdmin); {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, auth.ErrUnauthenticated):
				respondError(w, http.StatusUnauthorized, "unauthenticated", "not authenticated")
			default:
				respondError(w, http.StatusForbidden, "forbidden", "requires app admin or org admin role")
			}
		})
	}
}

// deprecationSuccessor names the surface that replaces the REST shim, carried
// in the Link header's successor-version relation. It is the Connect contract
// root; a specific procedure is the service path plus the method.
const deprecationSuccessor = "/shepherd.mgmt.v1"

// deprecationHeaders marks a response as coming from the deprecated REST shim.
// It sets Deprecation (RFC 9745), a successor-version Link (RFC 8288) pointing
// at the Connect contract, and a human-readable Warning (RFC 7234, code 299)
// so a person reading a raw response sees the migration note without consulting
// a spec. Removal is deliberately not a fixed Sunset date here — the removal
// release is scheduled, not calendar-dated — so no Sunset header is emitted.
func deprecationHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Deprecation", "true")
		h.Set("Link", "<"+deprecationSuccessor+">; rel=\"successor-version\"")
		h.Set("Warning", `299 - "Deprecated: the /api REST shim is replaced by the shepherd.mgmt.v1 Connect API and will be removed a release after v0.9.0; migrate machine callers to Connect."`)
		next.ServeHTTP(w, r)
	})
}

// Router builds the /api chi sub-router.
func Router(st *store.Store, cfg *config.Config, enc *crypto.Encryptor, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	v := validate.New(&cfg.Validate)

	// Schema registry: embedded artifacts + overlay.
	// ALLOY_VERSION from versions.env is baked in at build time via the ldflags/version package;
	// we use a build-time constant here. If the registry fails to initialize (corrupt embed),
	// schema endpoints return 503 — the rest of the API is unaffected.
	schemaVersion := version.AlloySchemaVersion
	schemaReg, schemaErr := schema.New(schema.Embedded, schemaVersion)
	schemaHandler := NewSchemaHandler(schemaReg) // nil-safe: handler checks schemaErr below

	_ = schemaErr // handled per-request below

	logger = logger.With("component", "mgmtapi")
	// pipelines is a thin REST shim over PipelineService (rpc_pipeline.go),
	// which owns all pipeline business logic; see pipelines.go.
	pipelines := NewPipelinesHandler(NewPipelineService(st, v, schemaReg, logger, WithBeaconRemoteWrite(cfg.Server.BaseURL, beacon.OAuth2ForBeacon(cfg.OIDC.BeaconAuth, cfg.OIDC.AgentTokenURL, cfg.OIDC.AgentScopes))))
	admin := NewAdminHandler(st, logger)
	orgs := NewOrgsHandler(st, logger)
	audit := NewAuditHandler(st, logger)
	wizards := NewWizardHandler(st, v, logger)
	visualHandler := NewVisualHandler(st, v, schemaReg, logger)
	simulateHandler := NewSimulateHandler(st, cfg.Simulator, logger)
	// repoLinks is always constructed; GitOpsService itself degrades to
	// empty lists / connect.CodeUnavailable when enc is nil (see
	// rpc_gitops.go), replicating the nil-encryptor guard this router used
	// to apply inline.
	repoLinks := NewRepoLinksHandler(NewGitOpsService(st, enc, logger))

	r := chi.NewRouter()

	// Every /api route is a legacy REST shim over the shepherd.mgmt.v1 Connect
	// contract (see doc.go). The shim is scheduled for removal: it is announced
	// deprecated in the v0.9.0 changelog and will be removed a release later.
	// This middleware marks every shim response so an external integration
	// still calling plain JSON is told, in-band, to move to Connect — the live
	// UI already speaks Connect (web/src/api/transport.ts) and is unaffected.
	// The headers are advisory (RFC 8594 / RFC 9745 style) and change no
	// behaviour, so callers keep working until the routes are actually removed.
	r.Use(deprecationHeaders)

	// NotFound: unmatched /api/* paths return 404 JSON instead of the default chi response.
	apiNotFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`)) //nolint:errcheck
	})
	r.NotFound(apiNotFound)
	r.MethodNotAllowed(apiNotFound)

	// Profile
	r.Get("/me", orgs.Me)

	// Schema endpoints — accessible to any authenticated user (reader role).
	// GET /api/schema/current → pinned fleet version merged with overlay.
	// GET /api/schema/{version} → specific version merged with overlay (ETag cached).
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/schema/current", schemaHandler.GetCurrent)
		r.Get("/schema/{version}", schemaHandler.Get)
	})

	// Admin endpoints — app admin only, except SearchGroups (app admin or
	// org admin of the org named in ?org_id=), mirroring
	// procedureRequirements in rpc_interceptor.go exactly.
	r.Route("/admin", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAppAdmin)
			r.Get("/orgs", admin.ListOrgs)
			r.Post("/orgs", admin.CreateOrg)
			r.Patch("/orgs/{org}", admin.UpdateOrg)
			r.Delete("/orgs/{org}", admin.DeleteOrg)

			r.Get("/clusters", admin.ListClusters)
			r.Post("/clusters/{cluster}/claim", admin.ClaimCluster)
			r.Post("/clusters/{cluster}/unclaim", admin.UnclaimCluster)

			r.Get("/agent-tokens", admin.ListTokens)
			r.Post("/agent-tokens", admin.CreateToken)
			r.Delete("/agent-tokens/{id}", admin.RevokeToken)
		})

		r.Group(func(r chi.Router) {
			r.Use(requireAppOrOrgAdmin(st))
			r.Get("/groups/search", admin.SearchGroups)
		})
	})

	// Org-scoped endpoints. Each group's role requirement mirrors the
	// corresponding procedure's entry in procedureRequirements
	// (rpc_interceptor.go) / the Services table in
	// docs/archive/api-contract-design.md.
	r.Route("/orgs/{org}", func(r chi.Router) {
		// org-reader: read-only routes.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "reader"))
			r.Get("/collectors", orgs.ListCollectors)
			r.Get("/collectors/{id}", orgs.GetCollector)
			r.Get("/collectors/{id}/served-config", orgs.ServedConfig)
			r.Get("/attributes", orgs.ListAttributes)

			r.Get("/pipelines", pipelines.List)
			r.Get("/pipelines/{id}", pipelines.Get)
			r.Get("/pipelines/{id}/preview-matches", pipelines.PreviewMatches)
			r.Get("/pipelines/{id}/revisions", pipelines.ListRevisions)
			r.Get("/pipelines/{id}/revisions/{rev}", pipelines.GetRevision)

			r.Get("/destinations", orgs.ListDestinations)
			r.Get("/destinations/{id}", orgs.GetDestination)
		})

		// org-editor: pipeline writes/validate and WizardService (D5 — the
		// REST shim's authoring routes now match Connect's org-editor floor
		// for these two services; see the org-admin group below for why
		// pipeline writes are STILL stricter here than Connect's own
		// org-reader-plus-ownership check).
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "orgeditor"))
			r.Post("/pipelines", pipelines.Create)
			r.Post("/pipelines/validate", pipelines.Validate)
			r.Put("/pipelines/{id}", pipelines.Update)
			r.Delete("/pipelines/{id}", pipelines.Delete)
			r.Post("/pipelines/{id}/enable", pipelines.Enable)
			r.Post("/pipelines/{id}/disable", pipelines.Disable)
			r.Post("/pipelines/{id}/revisions/{rev}/restore", pipelines.RestoreRevision)

			r.Get("/wizards", wizards.ListWizards)
			r.Get("/wizards/{kind}", wizards.GetWizardSchema)
			r.Post("/wizards/render", wizards.RenderWizard)
			r.Post("/wizards/commit", wizards.CommitWizard)
		})

		// org-editor: VisualService (except GraphView, org-reader below;
		// D5, same alignment as pipelines/wizards above).
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "orgeditor"))
			r.Post("/visual/render", visualHandler.Render)
			r.Post("/visual/validate", visualHandler.Validate)
			r.Post("/visual/upgrade-check", visualHandler.UpgradeCheck)
		})

		// org-admin: writes to what the org IS, plus the org-admin-only
		// services (GitOpsService, AuditService).
		//
		// W10 note — this group is deliberately NOT in step with
		// procedureRequirements any more, and the difference is safe in one
		// direction only. The pipeline procedures were loosened to
		// org-reader on the RPC side so a team member can reach the handler
		// and have AuthorizeOwnership make the fine-grained call
		// (docs/gateway-tier-plan.md G11); the org-editor group above still
		// demands org-editor for the same REST routes. So the shim is
		// STRICTER, never looser — a team member who is not at least an
		// editor gets 403 here rather than scoped write. That is a missing
		// feature on a legacy surface, not a bypass: the live UI speaks
		// Connect RPC (web/src/api/transport.ts), and a service account
		// cannot reach this router at all, since SessionMiddleware only
		// reads the session cookie and never inspects Authorization.
		//
		// Recorded rather than silently fixed because aligning it means
		// duplicating ownership resolution in the REST shim, and the shim is
		// on its way out. If it survives, that duplication is the work.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "orgadmin"))
			r.Get("/collectors/{id}/assignments", orgs.ListAssignments)
			r.Post("/collectors/{id}/assignments", orgs.CreateAssignment)
			r.Delete("/collectors/{id}/assignments/{group_id}", orgs.DeleteAssignment)

			r.Post("/destinations", orgs.CreateDestination)
			r.Put("/destinations/{id}", orgs.UpdateDestination)
			r.Delete("/destinations/{id}", orgs.DeleteDestination)

			r.Get("/audit", audit.List)

			// Renamed from /ado-credentials alongside the AdoCredential ->
			// GitCredential proto rename (docs/git-provider-design.md
			// §3.4): a deliberate breaking REST change, justified the same
			// way as the Connect rename — nothing in production consumes
			// the old path.
			r.Get("/git-credentials", repoLinks.ListCredentials)
			r.Post("/git-credentials", repoLinks.CreateCredential)
			r.Delete("/git-credentials/{id}", repoLinks.DeleteCredential)
			r.Post("/git-credentials/{id}/test", repoLinks.TestCredential)

			r.Get("/repo-links", repoLinks.ListRepoLinks)
			r.Post("/repo-links", repoLinks.CreateRepoLink)
			r.Delete("/repo-links/{id}", repoLinks.DeleteRepoLink)
		})

		// org-editor: SimulateService (D6 — settled at org-editor on every
		// layer: Connect's procedureRequirements already gated it here,
		// simulate.proto's comments and docs/visual-builder-design-VB1.md
		// now say so too).
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "orgeditor"))
			r.Post("/simulate/relabel", simulateHandler.SimulateRelabel)
			r.Post("/simulate/logs", simulateHandler.SimulateLogs)
			r.Post("/simulate/runs", simulateHandler.CreateRun)
			r.Get("/simulate/runs/{id}", simulateHandler.GetRun)
		})

		// org-reader: VisualService.GraphView.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "reader"))
			r.Get("/pipelines/{id}/graph", visualHandler.GraphView)
		})
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
// alongside r.Mount("/api", Router(...)).
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
			return mgmtv1connect.NewFleetServiceHandler(NewFleetService(st, logger), authz...)
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
			return mgmtv1connect.NewTenantRouteServiceHandler(NewTenantRouteService(st, logger), authz...)
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
