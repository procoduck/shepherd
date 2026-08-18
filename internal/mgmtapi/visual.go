package mgmtapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

type VisualHandler struct { //nolint:revive
	store     *store.Store
	validator *validate.Validator
	schema    *schema.Registry
	logger    *slog.Logger
}

func NewVisualHandler(st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger) *VisualHandler { //nolint:revive
	return &VisualHandler{store: st, validator: v, schema: reg, logger: logger}
}

type visualRequest struct {
	Graph visual.GraphDocument `json:"graph"`
}
type visualResponse struct {
	Content     string                      `json:"content"`
	NodeMap     map[string]visual.NodeRange `json:"node_map"`
	Diagnostics []any                       `json:"diagnostics"`
}

func (h *VisualHandler) render(r *http.Request) (visual.RenderResult, error) {
	var req visualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return visual.RenderResult{}, err
	}
	if h.schema == nil {
		return visual.RenderResult{}, errSchemaUnavailable
	}
	payload, _, err := h.schema.Get(req.Graph.SchemaVersion)
	if err != nil {
		return visual.RenderResult{}, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return visual.RenderResult{}, err
	}
	var schemaPayload visual.SchemaPayload
	if err := json.Unmarshal(b, &schemaPayload); err != nil {
		return visual.RenderResult{}, err
	}
	return visual.Render(req.Graph, schemaPayload), nil
}

func (h *VisualHandler) Render(w http.ResponseWriter, r *http.Request) { //nolint:revive
	result, err := h.render(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(result.Diagnostics) > 0 {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "render_error"}, "diagnostics": result.Diagnostics})
		return
	}
	respondJSON(w, http.StatusOK, visualResponse{Content: result.Content, NodeMap: result.NodeMap, Diagnostics: []any{}})
}

func (h *VisualHandler) Validate(w http.ResponseWriter, r *http.Request) { //nolint:revive
	result, err := h.render(r)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(result.Diagnostics) > 0 {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]any{"diagnostics": result.Diagnostics})
		return
	}
	vr := h.validator.Stages12(r.Context(), result.Content)
	out := make([]map[string]any, 0, len(vr.Diagnostics))
	for _, d := range vr.Diagnostics {
		id := ""
		for node, rg := range result.NodeMap {
			if d.Line >= rg.StartLine && d.Line <= rg.EndLine {
				id = node
				break
			}
		}
		out = append(out, map[string]any{"layer": "L2", "node_id": id, "line": d.Line, "col": d.Col, "message": d.Message})
	}
	respondJSON(w, http.StatusOK, map[string]any{"diagnostics": out})
}

// UpgradeCheck handles POST /api/orgs/{org}/visual/upgrade-check.
// upgradeCheckMaxBodyBytes caps upgrade-check request bodies at 512 KiB.
const upgradeCheckMaxBodyBytes = 512 * 1024

// UpgradeCheck handles POST /api/orgs/{org}/visual/upgrade-check.
// Computes a structural diff of the graph against the current schema version.
func (h *VisualHandler) UpgradeCheck(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, upgradeCheckMaxBodyBytes)
	var req visualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if h.schema == nil {
		respondError(w, http.StatusServiceUnavailable, "unavailable", errSchemaUnavailable.Error())
		return
	}

	oldVersion := req.Graph.SchemaVersion
	if oldVersion == "" {
		oldVersion = h.schema.CurrentVersion()
	}
	newVersion := h.schema.CurrentVersion()

	oldPayload, _, err := h.schema.Get(oldVersion)
	if err != nil {
		if schema.IsNotFound(err) {
			respondError(w, http.StatusBadRequest, "bad_request", "schema version not found: "+oldVersion)
			return
		}
		respondError(w, http.StatusServiceUnavailable, "unavailable", "schema unavailable")
		return
	}
	newPayload, _, err := h.schema.Get(newVersion)
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, "unavailable", "current schema unavailable")
		return
	}

	// Merge migrations from overlay into the new payload before marshaling.
	if migs := h.schema.Migrations(); len(migs) > 0 {
		if np, ok := newPayload["migrations"]; !ok || np == nil {
			merged := make(map[string]any, len(newPayload)+1)
			for k, v := range newPayload {
				merged[k] = v
			}
			merged["migrations"] = migs
			newPayload = merged
		}
	}

	// Convert map[string]any → UpgradeSchemaPayload via JSON round-trip.
	var oldSchema, newSchema visual.UpgradeSchemaPayload
	if b, err := json.Marshal(oldPayload); err == nil {
		_ = json.Unmarshal(b, &oldSchema) //nolint:errcheck
	}
	if b, err := json.Marshal(newPayload); err == nil {
		_ = json.Unmarshal(b, &newSchema) //nolint:errcheck
	}

	result := visual.UpgradeCheck(req.Graph, oldSchema, newSchema, oldVersion, newVersion)
	respondJSON(w, http.StatusOK, result)
}

var errSchemaUnavailable = &schemaError{}

type schemaError struct{}

func (*schemaError) Error() string { return "schema registry not initialized" }

// GraphView handles GET /api/orgs/{org}/pipelines/{id}/graph.
// Returns a best-effort GraphDocument extracted from any pipeline's Alloy text.
// [reader] — accessible to org readers (graph view is read-only).
func (h *VisualHandler) GraphView(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipelineForGraph(w, r)
	if !ok {
		return
	}

	schemaVersion := "alloy-v" + strings.TrimPrefix(h.schema.CurrentVersion(), "alloy-v")
	if schemaVersion == "alloy-" {
		schemaVersion = "alloy-v1.18.1"
	}

	result := visual.ParseAlloy(p.Contents, schemaVersion)
	respondJSON(w, http.StatusOK, map[string]any{
		"graph":   result.Doc,
		"opaque":  result.Opaque,
		"warning": result.Warning,
	})
}

// loadPipelineForGraph loads a pipeline by {id} URL param, responding with 404 on miss.
func (h *VisualHandler) loadPipelineForGraph(w http.ResponseWriter, r *http.Request) (sqlc.Pipeline, bool) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid pipeline id")
		return sqlc.Pipeline{}, false
	}
	p, err := h.store.Queries.GetPipelineByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", "pipeline not found")
		return sqlc.Pipeline{}, false
	}
	orgID := orgIDFromParam(r)
	if !orgID.Valid {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return sqlc.Pipeline{}, false
	}
	if p.OrgID != orgID {
		respondError(w, http.StatusNotFound, "not_found", "pipeline not found")
		return sqlc.Pipeline{}, false
	}
	return p, true
}
