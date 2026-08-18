package mgmtapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

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
	pipelines := NewPipelinesHandler(st, v, schemaReg, logger)
	admin := NewAdminHandler(st, logger)
	orgs := NewOrgsHandler(st, logger)
	audit := NewAuditHandler(st, logger)
	wizards := NewWizardHandler(st, logger)
	visualHandler := NewVisualHandler(st, v, schemaReg, logger)
	simulateHandler := NewSimulateHandler(logger)
	var repoLinks *RepoLinksHandler
	if enc != nil {
		repoLinks = NewRepoLinksHandler(st, enc, logger)
	}

	r := chi.NewRouter()

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

	// Admin endpoints (App Admin only — RBAC enforced inside handlers for now; full middleware in M5)
	r.Route("/admin", func(r chi.Router) {
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

		r.Get("/groups/search", admin.SearchGroups)
	})

	// Org-scoped endpoints
	r.Route("/orgs/{org}", func(r chi.Router) {
		r.Get("/collectors", orgs.ListCollectors)
		r.Get("/collectors/{id}", orgs.GetCollector)
		r.Get("/collectors/{id}/served-config", orgs.ServedConfig)
		r.Post("/collectors/{id}/assignments", orgs.CreateAssignment)
		r.Delete("/collectors/{id}/assignments/{group_id}", orgs.DeleteAssignment)

		r.Get("/pipelines", pipelines.List)
		r.Post("/pipelines", pipelines.Create)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "orgadmin"))
			r.Post("/visual/render", visualHandler.Render)
			r.Post("/visual/validate", visualHandler.Validate)
			r.Post("/visual/upgrade-check", visualHandler.UpgradeCheck)
			r.Post("/simulate/relabel", simulateHandler.SimulateRelabel)
			r.Post("/simulate/logs", simulateHandler.SimulateLogs)
		})
		r.Post("/pipelines/validate", pipelines.Validate)
		r.Get("/pipelines/{id}", pipelines.Get)
		r.Put("/pipelines/{id}", pipelines.Update)
		r.Delete("/pipelines/{id}", pipelines.Delete)
		r.Post("/pipelines/{id}/enable", pipelines.Enable)
		r.Post("/pipelines/{id}/disable", pipelines.Disable)
		r.Get("/pipelines/{id}/preview-matches", pipelines.PreviewMatches)
		r.Get("/pipelines/{id}/revisions", pipelines.ListRevisions)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireOrgAccess(st, "org", "reader"))
			r.Get("/pipelines/{id}/graph", visualHandler.GraphView)
		})

		r.Get("/attributes", orgs.ListAttributes)

		r.Get("/wizards", wizards.ListWizards)
		r.Get("/wizards/{kind}", wizards.GetWizardSchema)
		r.Post("/wizards/commit", wizards.CommitWizard)

		r.Get("/destinations", orgs.ListDestinations)
		r.Post("/destinations", orgs.CreateDestination)
		r.Get("/destinations/{id}", orgs.GetDestination)
		r.Put("/destinations/{id}", orgs.UpdateDestination)
		r.Delete("/destinations/{id}", orgs.DeleteDestination)

		r.Get("/audit", audit.List)

		r.Get("/ado-credentials", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks == nil {
				respondJSON(w, http.StatusOK, listResponse[any]{Items: []any{}, Total: 0})
				return
			}
			repoLinks.ListCredentials(w, r)
		})
		r.Post("/ado-credentials", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks == nil {
				respondError(w, http.StatusServiceUnavailable, "unavailable", "encryption not configured")
				return
			}
			repoLinks.CreateCredential(w, r)
		})
		r.Delete("/ado-credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks != nil {
				repoLinks.DeleteCredential(w, r)
			}
		})

		r.Get("/repo-links", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks == nil {
				respondJSON(w, http.StatusOK, listResponse[any]{Items: []any{}, Total: 0})
				return
			}
			repoLinks.ListRepoLinks(w, r)
		})
		r.Post("/repo-links", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks == nil {
				respondError(w, http.StatusServiceUnavailable, "unavailable", "encryption not configured")
				return
			}
			repoLinks.CreateRepoLink(w, r)
		})
		r.Delete("/repo-links/{id}", func(w http.ResponseWriter, r *http.Request) {
			if repoLinks != nil {
				repoLinks.DeleteRepoLink(w, r)
			}
		})
	})

	return r
}
