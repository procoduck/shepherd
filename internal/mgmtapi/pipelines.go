package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/auth"
	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// PipelinesHandler handles /api/orgs/{org}/pipelines routes.
type PipelinesHandler struct {
	store     *store.Store
	validator *validate.Validator
	schema    *schema.Registry
	logger    *slog.Logger
}

// NewPipelinesHandler creates a PipelinesHandler.
func NewPipelinesHandler(st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger) *PipelinesHandler {
	return &PipelinesHandler{store: st, validator: v, schema: reg, logger: logger}
}

// pipelineRequest is the request body for create/update.
type pipelineRequest struct {
	Name        string          `json:"name"`
	Contents    string          `json:"contents"`
	Matchers    []string        `json:"matchers"`
	Source      string          `json:"source,omitempty"`
	WizardState json.RawMessage `json:"wizard_state,omitempty"`
}

// checkVisualRenderMatch re-renders wizard_state and compares it with clientContent.
func (h *PipelinesHandler) checkVisualRenderMatch(_ context.Context, clientContent string, wizardState json.RawMessage) (bool, error) {
	if h.schema == nil {
		return false, nil
	}
	var doc visual.GraphDocument
	if err := json.Unmarshal(wizardState, &doc); err != nil {
		return false, fmt.Errorf("unmarshal wizard_state: %w", err)
	}
	version := doc.SchemaVersion
	if version == "" {
		version = h.schema.CurrentVersion()
	}
	payload, _, err := h.schema.Get(version)
	if err != nil {
		return false, fmt.Errorf("load schema %q: %w", version, err)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal schema: %w", err)
	}
	var schemaPayload visual.SchemaPayload
	if err := json.Unmarshal(b, &schemaPayload); err != nil {
		return false, fmt.Errorf("unmarshal schema payload: %w", err)
	}
	result := visual.Render(doc, schemaPayload)
	if len(result.Diagnostics) > 0 {
		return false, nil
	}
	return result.Content != clientContent, nil
}

// pipelineResponse is the API representation of a pipeline.
type pipelineResponse struct {
	ID        string             `json:"id"`
	OrgID     string             `json:"org_id"`
	Name      string             `json:"name"`
	Contents  string             `json:"contents"`
	Matchers  []string           `json:"matchers"`
	Enabled   bool               `json:"enabled"`
	Source    string             `json:"source"`
	Revision  int                `json:"revision,omitempty"`
	CreatedBy string             `json:"created_by"`
	UpdatedBy string             `json:"updated_by"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
	Revisions []revisionResponse `json:"revisions,omitempty"`
}

type revisionResponse struct {
	Revision   int    `json:"revision"`
	ChangedBy  string `json:"changed_by"`
	ChangedAt  string `json:"changed_at"`
	ChangeNote string `json:"change_note"`
}

func pipelineToResponse(p sqlc.Pipeline) pipelineResponse {
	idStr := p.ID.String()
	orgIDStr := p.OrgID.String()
	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		matchers = []string{}
	}
	if matchers == nil {
		matchers = []string{}
	}
	return pipelineResponse{
		ID:        idStr,
		OrgID:     orgIDStr,
		Name:      p.Name,
		Contents:  p.Contents,
		Matchers:  matchers,
		Enabled:   p.Enabled,
		Source:    p.Source,
		CreatedBy: p.CreatedBy,
		UpdatedBy: p.UpdatedBy,
		CreatedAt: p.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt: p.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// List GET /api/orgs/{org}/pipelines
func (h *PipelinesHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	if !orgID.Valid {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return
	}
	pipelines, err := h.store.Queries.ListPipelinesByOrg(r.Context(), orgID)
	if err != nil {
		h.logger.Error("list pipelines", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to list pipelines")
		return
	}

	// ?needs_upgrade=true — filter to visual pipelines whose schema_version < current.
	needsUpgrade := r.URL.Query().Get("needs_upgrade") == "true"
	if needsUpgrade && h.schema != nil {
		current := h.schema.CurrentVersion()
		filtered := pipelines[:0]
		for i := range pipelines {
			if pipelines[i].Source == "visual" && visualNeedsUpgrade(pipelines[i].WizardState, current) {
				filtered = append(filtered, pipelines[i])
			}
		}
		pipelines = filtered
	}

	items := make([]pipelineResponse, len(pipelines))
	for i := range pipelines {
		p := pipelines[i]
		items[i] = pipelineToResponse(p)
	}
	respondJSON(w, http.StatusOK, listResponse[pipelineResponse]{Items: items, Total: len(items)})
}

// visualNeedsUpgrade reports whether the wizard_state JSON contains a schema_version
// that differs from currentVersion.
func visualNeedsUpgrade(wizardState []byte, currentVersion string) bool {
	if len(wizardState) == 0 {
		return false
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(wizardState, &doc); err != nil {
		return false
	}
	return doc.SchemaVersion != "" && doc.SchemaVersion != currentVersion
}

// Create POST /api/orgs/{org}/pipelines
func (h *PipelinesHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	if !orgID.Valid {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return
	}
	var req pipelineRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	if req.Source == "visual" && len(req.WizardState) > 0 {
		mismatch, checkErr := h.checkVisualRenderMatch(r.Context(), req.Contents, req.WizardState)
		if checkErr != nil {
			h.logger.Warn("visual render-equality check failed", "err", checkErr)
		} else if mismatch {
			respondJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "render_mismatch", "message": "submitted content does not match server render of the graph; use POST /visual/render to get the canonical content"}})
			return
		}
	}

	matchersJSON, err := json.Marshal(req.Matchers)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid matchers")
		return
	}

	// Stage 1+2 validation.
	wrapped := validate.WrapForValidation(req.Name, req.Contents)
	if result := h.validator.Stages12(r.Context(), wrapped); !result.Valid {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":       map[string]any{"code": "validation_failed", "message": "pipeline failed validation"},
			"diagnostics": result.Diagnostics,
		})
		return
	}

	actor := actorFromCtx(r.Context())
	source := req.Source
	if source == "" {
		source = "ui"
	}
	p, err := h.store.Queries.CreatePipeline(r.Context(), sqlc.CreatePipelineParams{
		OrgID:       orgID,
		Name:        req.Name,
		Contents:    req.Contents,
		Matchers:    matchersJSON,
		Enabled:     false,
		Source:      source,
		WizardState: req.WizardState,
		CreatedBy:   actor,
		UpdatedBy:   actor,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Check if it's a sanitized-name collision (different names that map to same block name)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && strings.Contains(pgErr.ConstraintName, "sanitized_name") {
				respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error": map[string]any{"code": "name_collision", "message": "pipeline sanitized name collides with an existing pipeline"},
				})
			} else {
				respondError(w, http.StatusConflict, "conflict", "pipeline name already exists in this org")
			}
			return
		}
		h.logger.Error("create pipeline", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to create pipeline")
		return
	}

	// Create revision 1.
	_ = h.createRevision(r.Context(), p, "created", actor)
	_ = h.writeAudit(r.Context(), actor, orgID, "pipeline.create", uuidStr(p.ID))
	h.logger.Info("pipeline created", "pipeline_id", uuidStr(p.ID), "org_id", uuidStr(orgID), "name", p.Name, "actor", actor)

	respondJSON(w, http.StatusCreated, pipelineToResponse(p))
}

// Get GET /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Get(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	resp := pipelineToResponse(p)

	// Attach revisions.
	revs, err := h.store.Queries.ListPipelineRevisions(r.Context(), p.ID)
	if err != nil {
		h.logger.Warn("failed to load pipeline revisions", "err", err)
	}
	for i := range revs {
		rv := revs[i]
		resp.Revisions = append(resp.Revisions, revisionResponse{
			Revision:   int(rv.Revision),
			ChangedBy:  rv.ChangedBy,
			ChangedAt:  rv.ChangedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			ChangeNote: rv.ChangeNote,
		})
	}
	respondJSON(w, http.StatusOK, resp)
}

// Update PUT /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Update(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	if p.Source == "git" {
		respondError(w, http.StatusForbidden, "forbidden", "git-sourced pipelines are read-only")
		return
	}
	var req pipelineRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	if req.Source == "visual" && len(req.WizardState) > 0 {
		mismatch, checkErr := h.checkVisualRenderMatch(r.Context(), req.Contents, req.WizardState)
		if checkErr != nil {
			h.logger.Warn("visual render-equality check failed", "err", checkErr)
		} else if mismatch {
			respondJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "render_mismatch", "message": "submitted content does not match server render of the graph; use POST /visual/render to get the canonical content"}})
			return
		}
	}

	matchersJSON, err := json.Marshal(req.Matchers)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid matchers")
		return
	}

	// Stage 1+2 validation.
	wrapped := validate.WrapForValidation(req.Name, req.Contents)
	if result := h.validator.Stages12(r.Context(), wrapped); !result.Valid {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":       map[string]any{"code": "validation_failed", "message": "pipeline failed validation"},
			"diagnostics": result.Diagnostics,
		})
		return
	}

	actor := actorFromCtx(r.Context())
	updated, err := h.store.Queries.UpdatePipeline(r.Context(), sqlc.UpdatePipelineParams{
		ID:        p.ID,
		Name:      req.Name,
		Contents:  req.Contents,
		Matchers:  matchersJSON,
		UpdatedBy: actor,
	})
	if err != nil {
		h.logger.Error("update pipeline", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to update pipeline")
		return
	}

	_ = h.createRevision(r.Context(), updated, "updated", actor)

	// If enabled, dirty the serve cache for affected collectors.
	if updated.Enabled {
		orgID := orgIDFromParam(r)
		_ = h.store.Queries.MarkServeCacheDirtyByOrg(r.Context(), orgID)
		go h.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context
	}

	_ = h.writeAudit(r.Context(), actor, orgIDFromParam(r), "pipeline.update", uuidStr(p.ID))
	respondJSON(w, http.StatusOK, pipelineToResponse(updated))
}

// Enable POST /api/orgs/{org}/pipelines/{id}/enable
func (h *PipelinesHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

// Disable POST /api/orgs/{org}/pipelines/{id}/disable
func (h *PipelinesHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *PipelinesHandler) setEnabled(w http.ResponseWriter, r *http.Request, enable bool) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	orgID := orgIDFromParam(r)
	actor := actorFromCtx(r.Context())

	if enable {
		// Stage 3: dry-run merge with candidate included — validates new state.
		if err := h.stage3Check(r.Context(), p, orgID, true); err != nil {
			respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": map[string]any{"code": "merge_validation_failed", "message": err.Error()},
			})
			return
		}
	} else {
		// On disable: validate collectors that previously matched — ensures their
		// remaining pipelines still produce a valid merge (covers OLD matcher set per spec §P1.2).
		if err := h.stage3Check(r.Context(), p, orgID, false); err != nil {
			respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": map[string]any{"code": "merge_validation_failed", "message": err.Error()},
			})
			return
		}
	}

	updated, err := h.store.Queries.SetPipelineEnabled(r.Context(), sqlc.SetPipelineEnabledParams{
		ID:        p.ID,
		Enabled:   enable,
		UpdatedBy: actor,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to update pipeline")
		return
	}

	_ = h.store.Queries.MarkServeCacheDirtyByOrg(r.Context(), orgID)
	// Recompute serve-cache eagerly for all collectors in this org so the
	// next GetConfig poll (and the REST served-config endpoint) reflects the
	// updated pipeline state immediately.
	go h.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context — recompute runs after request completes
	action := "pipeline.disable"
	if enable {
		action = "pipeline.enable"
	}
	_ = h.writeAudit(r.Context(), actor, orgID, action, uuidStr(p.ID))
	h.logger.Info("pipeline enabled/disabled", "pipeline_id", uuidStr(p.ID), "org_id", uuidStr(orgID), "enabled", enable, "actor", actor)
	respondJSON(w, http.StatusOK, pipelineToResponse(updated))
}

// Delete DELETE /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	if p.Source == "git" {
		respondError(w, http.StatusForbidden, "forbidden", "git-sourced pipelines are read-only")
		return
	}
	actor := actorFromCtx(r.Context())
	orgID := orgIDFromParam(r)
	if err := h.store.Queries.DeletePipeline(r.Context(), p.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to delete pipeline")
		return
	}
	_ = h.store.Queries.MarkServeCacheDirtyByOrg(r.Context(), orgID)
	go h.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context
	_ = h.writeAudit(r.Context(), actor, orgID, "pipeline.delete", uuidStr(p.ID))
	h.logger.Info("pipeline deleted", "pipeline_id", uuidStr(p.ID), "org_id", uuidStr(orgID), "actor", actor)
	w.WriteHeader(http.StatusNoContent)
}

// ListRevisions GET /api/orgs/{org}/pipelines/{id}/revisions
func (h *PipelinesHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	revs, err := h.store.Queries.ListPipelineRevisions(r.Context(), p.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to load revisions")
		return
	}
	items := make([]revisionResponse, len(revs))
	for i := range revs {
		rv := revs[i]
		var m []string
		if jsonErr := json.Unmarshal(rv.Matchers, &m); jsonErr != nil {
			h.logger.Debug("list revisions: unmarshal matchers", "err", jsonErr)
		}
		items[i] = revisionResponse{
			Revision:   int(rv.Revision),
			ChangedBy:  rv.ChangedBy,
			ChangedAt:  rv.ChangedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
			ChangeNote: rv.ChangeNote,
		}
	}
	respondJSON(w, http.StatusOK, listResponse[revisionResponse]{Items: items, Total: len(items)})
}

// Validate POST /api/orgs/{org}/pipelines/validate
func (h *PipelinesHandler) Validate(w http.ResponseWriter, r *http.Request) {
	var req pipelineRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := req.Name
	if name == "" {
		name = "preview"
	}
	wrapped := validate.WrapForValidation(name, req.Contents)
	result := h.validator.Stages12(r.Context(), wrapped)
	status := http.StatusOK
	if !result.Valid {
		status = http.StatusUnprocessableEntity
	}
	respondJSON(w, status, result)
}

// PreviewMatches GET /api/orgs/{org}/pipelines/{id}/preview-matches
func (h *PipelinesHandler) PreviewMatches(w http.ResponseWriter, r *http.Request) {
	p, ok := h.loadPipeline(w, r)
	if !ok {
		return
	}
	orgID := orgIDFromParam(r)

	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		h.logger.Debug("unmarshal matchers", "err", err)
	}

	mp := merge.Pipeline{
		ID:       uuidStr(p.ID),
		Name:     p.Name,
		Matchers: matchers,
		Source:   p.Source,
	}

	matched, err := h.previewMatchedCollectors(r.Context(), mp, orgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"collectors": matched})
}

// --- helpers ---

func (h *PipelinesHandler) loadPipeline(w http.ResponseWriter, r *http.Request) (sqlc.Pipeline, bool) {
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
	return p, true
}

func (h *PipelinesHandler) createRevision(ctx context.Context, p sqlc.Pipeline, note, actor string) error {
	maxRev, _ := h.store.Queries.GetMaxPipelineRevision(ctx, p.ID) //nolint:errcheck // returns 0 on err, which is the safe default
	_, err := h.store.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
		PipelineID: p.ID,
		Revision:   maxRev + 1,
		Contents:   p.Contents,
		Matchers:   p.Matchers,
		Enabled:    p.Enabled,
		ChangedBy:  actor,
		ChangeNote: note,
	})
	return err
}

func (h *PipelinesHandler) writeAudit(ctx context.Context, actor string, orgID pgtype.UUID, action, resID string) error {
	return h.store.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor:        actor,
		ActorType:    "user",
		OrgID:        orgID,
		Action:       action,
		ResourceType: "pipeline",
		ResourceID:   resID,
		Detail:       json.RawMessage("{}"),
	})
}

// stage3Check runs Stage 3 validation: assembles merged configs for all affected collectors,
// deduplicates by content hash, then runs Stages 1 and 2 on each unique content
// with concurrency=4 and a configurable timeout (validate.stage3_timeout).
func (h *PipelinesHandler) stage3Check(ctx context.Context, p sqlc.Pipeline, orgID pgtype.UUID, includeCandidate bool) error {
	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		h.logger.Debug("stage3: unmarshal matchers", "err", err)
	}

	timeout := h.validator.Stage3Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	enabledPipelines, err := h.store.Queries.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("loading pipelines: %w", err)
	}

	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID:       uuidStr(ep.ID),
			Name:     ep.Name,
			Contents: ep.Contents,
			Matchers: m,
			Source:   ep.Source,
		})
	}
	pID := uuidStr(p.ID)
	if includeCandidate {
		// On enable: add/replace candidate in the merge set.
		found := false
		for i, mp := range mergePipelines {
			if mp.ID == pID {
				mergePipelines[i] = merge.Pipeline{ID: pID, Name: p.Name, Contents: p.Contents, Matchers: matchers, Source: p.Source}
				found = true
				break
			}
		}
		if !found {
			mergePipelines = append(mergePipelines, merge.Pipeline{
				ID: pID, Name: p.Name, Contents: p.Contents, Matchers: matchers, Source: p.Source,
			})
		}
	} else {
		// On disable: remove candidate from merge set (validate remaining pipelines).
		filtered := mergePipelines[:0]
		for _, mp := range mergePipelines {
			if mp.ID != pID {
				filtered = append(filtered, mp)
			}
		}
		mergePipelines = filtered
	}

	collectors, err := h.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("loading collectors: %w", err)
	}

	// Assemble merged content for every collector; deduplicate by sha256 hash.
	type mergedEntry struct {
		content      string
		collectorKey string // "cluster/role" for error messages
	}
	seen := make(map[string]bool) // hash → already validated
	var toValidate []mergedEntry
	var failures []string

	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: uuidStr(c.ID),
			Labels:      map[string]string{"role": c.Role},
		}
		cluster, _ := h.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty cluster name is safe in merge
		cl.Labels["cluster"] = cluster.Name
		key := cluster.Name + "/" + c.Role

		result, assembleErr := merge.Assemble(uuidStr(c.ID), key, cl, mergePipelines, "dev", "")
		if assembleErr != nil {
			failures = append(failures, fmt.Sprintf("%s: merge error: %v", key, assembleErr))
			continue
		}
		if !seen[result.Hash] {
			seen[result.Hash] = true
			toValidate = append(toValidate, mergedEntry{content: result.Content, collectorKey: key})
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("merge validation failed on collectors: %s", strings.Join(failures, " | "))
	}

	// Validate unique merged contents concurrently (max 4 workers).
	type result struct {
		key  string
		msgs []string
	}
	sem := make(chan struct{}, 4)
	resultCh := make(chan result, len(toValidate))

	for _, entry := range toValidate {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			var msgs []string

			r1 := validate.Stage1(entry.content)
			if !r1.Valid {
				for _, d := range r1.Diagnostics {
					msgs = append(msgs, fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message))
				}
				resultCh <- result{key: entry.collectorKey, msgs: msgs}
				return
			}

			r2 := h.validator.Stage2(ctx, entry.content)
			if !r2.Valid {
				for _, d := range r2.Diagnostics {
					msgs = append(msgs, fmt.Sprintf("stage2:%d:%d %s", d.Line, d.Col, d.Message))
				}
			}
			resultCh <- result{key: entry.collectorKey, msgs: msgs}
		}()
	}

	// Collect results.
	for range toValidate {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stage-3 validation budget exceeded: %w", ctx.Err())
		case r := <-resultCh:
			if len(r.msgs) > 0 {
				failures = append(failures, fmt.Sprintf("%s: %s", r.key, strings.Join(r.msgs, "; ")))
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("merge validation failed on collectors: %s", strings.Join(failures, " | "))
	}
	return nil
}

func (h *PipelinesHandler) previewMatchedCollectors(ctx context.Context, p merge.Pipeline, orgID pgtype.UUID) ([]map[string]string, error) {
	collectors, err := h.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var matched []map[string]string
	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: uuidStr(c.ID),
			Labels:      map[string]string{"role": c.Role},
		}
		cluster, _ := h.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty cluster name is safe in merge
		cl.Labels["cluster"] = cluster.Name

		ok, err := merge.MatchesPipeline(p, cl)
		if err != nil || !ok {
			continue
		}
		matched = append(matched, map[string]string{"cluster": cluster.Name, "role": c.Role, "id": uuidStr(c.ID)})
	}
	return matched, nil
}

// orgIDFromParam extracts and parses the {org} URL param.
func orgIDFromParam(r *http.Request) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "org")); err != nil {
		id.Valid = false
	}
	return id
}

// actorFromCtx returns the authenticated actor identity from the request context.
// Returns "anonymous" if not set (should not happen in practice).
func actorFromCtx(ctx context.Context) string {
	return auth.ActorFromCtx(ctx)
}

// uuidStr converts a pgtype.UUID to its dash-separated string representation.
func uuidStr(id pgtype.UUID) string {
	return id.String()
}

// recomputeOrgCaches assembles and writes the serve cache for all collectors in an org.
// Called as a background goroutine after pipeline enable/disable/update.
func (h *PipelinesHandler) recomputeOrgCaches(ctx context.Context, orgID pgtype.UUID) {
	// Optional prewarm: use the same conditional cache path as lazy GetConfig.
	// It is panic-safe and caller-invisible; failures are logged and never affect HTTP responses.
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("recomputeOrgCaches panicked", "org_id", orgID.String(), "panic", r)
		}
	}()
	collectors, err := h.store.Queries.ListCollectorsWithClusterByOrg(ctx, orgID)
	if err != nil {
		h.logger.Warn("recomputeOrgCaches: listing collectors failed", "err", err)
		return
	}
	enabledPipelines, err := h.store.Queries.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		h.logger.Warn("recomputeOrgCaches: listing pipelines failed", "err", err)
		return
	}
	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID: uuidStr(ep.ID), Name: ep.Name, Contents: ep.Contents,
			Matchers: m, Source: ep.Source,
		})
	}
	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: uuidStr(c.ID),
			Labels:      map[string]string{"role": c.Role, "cluster": c.ClusterName},
		}
		result, assembleErr := merge.Assemble(uuidStr(c.ID), c.ClusterName+"/"+c.Role, cl, mergePipelines, "prod", "")
		if assembleErr != nil {
			h.logger.Warn("recomputeOrgCaches: merge failed", "collector_id", uuidStr(c.ID), "err", assembleErr)
			continue
		}
		r1 := validate.Stage1(result.Content)
		if !r1.Valid {
			h.logger.Warn("recomputeOrgCaches: stage-1 invalid on merged output", "collector_id", uuidStr(c.ID))
			continue
		}
		if _, upsertErr := h.store.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: c.ID,
			Content:     result.Content,
			Hash:        result.Hash,
		}); upsertErr != nil {
			h.logger.Warn("recomputeOrgCaches: upsert failed", "collector_id", uuidStr(c.ID), "err", upsertErr)
		}
	}
}
