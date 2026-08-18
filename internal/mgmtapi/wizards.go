package mgmtapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/appobservability" // register wizard
)

// WizardHandler handles /api/orgs/{org}/wizards routes.
type WizardHandler struct {
	store    *store.Store
	registry *wizard.Registry
	logger   *slog.Logger
}

// NewWizardHandler creates a wizard handler.
func NewWizardHandler(st *store.Store, logger *slog.Logger) *WizardHandler {
	return &WizardHandler{store: st, registry: wizard.Default(), logger: logger}
}

// ListWizards returns all registered wizard kinds.
func (h *WizardHandler) ListWizards(w http.ResponseWriter, r *http.Request) {
	kinds := h.registry.ListKinds() // ListKinds never fails
	type item struct {
		Kind  string        `json:"kind"`
		Title string        `json:"title"`
		Steps []wizard.Step `json:"steps"`
	}
	items := make([]item, 0, len(kinds))
	for _, k := range kinds {
		wiz, err := h.registry.Get(k)
		if err != nil {
			continue
		}
		s := wiz.Schema()
		items = append(items, item{Kind: s.Kind, Title: s.Title, Steps: s.Steps})
	}
	respondJSON(w, http.StatusOK, listResponse[item]{Items: items, Total: len(items)})
}

// GetWizardSchema returns the schema for a specific wizard kind.
func (h *WizardHandler) GetWizardSchema(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	wiz, err := h.registry.Get(kind)
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, wiz.Schema())
}

type commitRequest struct {
	Kind  string         `json:"kind"`
	Name  string         `json:"name"`
	State map[string]any `json:"state"`
}

// CommitWizard generates a pipeline from wizard state and creates it.
func (h *WizardHandler) CommitWizard(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)

	var req commitRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	wiz, err := h.registry.Get(req.Kind)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	result, err := wiz.Commit(req.State)
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "wizard_error", err.Error())
		return
	}

	stateJSON, err := wizard.MarshalState(req.State)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	matchersJSON, err := json.Marshal(result.Matchers)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "marshal error")
		return
	}

	actor := actorFromCtx(r.Context())
	p, err := h.store.Queries.CreatePipeline(r.Context(), sqlc.CreatePipelineParams{
		OrgID:       orgID,
		Name:        req.Name,
		Contents:    result.Contents,
		Matchers:    matchersJSON,
		Enabled:     false,
		Source:      "wizard",
		WizardKind:  pgtype.Text{String: req.Kind, Valid: true},
		WizardState: stateJSON,
		CreatedBy:   actor,
		UpdatedBy:   actor,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respondError(w, http.StatusConflict, "conflict", "pipeline name already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, "internal", "failed to create pipeline")
		return
	}

	respondJSON(w, http.StatusCreated, pipelineToResponse(p))
}
