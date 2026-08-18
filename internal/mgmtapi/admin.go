package mgmtapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// AdminHandler handles /api/admin routes.
type AdminHandler struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAdminHandler creates an admin route handler.
func NewAdminHandler(st *store.Store, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{store: st, logger: logger}
}

type orgRequest struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	AdminGroupID  string `json:"admin_group_id"`
	ReaderGroupID string `json:"reader_group_id"`
}

type orgResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	AdminGroupID  string `json:"admin_group_id"`
	ReaderGroupID string `json:"reader_group_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func orgToResponse(o sqlc.Org) orgResponse {
	id := o.ID.String()
	return orgResponse{
		ID:            id,
		Name:          o.Name,
		DisplayName:   o.DisplayName,
		AdminGroupID:  o.AdminGroupID,
		ReaderGroupID: o.ReaderGroupID.String,
		CreatedAt:     o.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:     o.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// ListOrgs lists organizations.
func (h *AdminHandler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	orgs, err := h.store.Queries.ListOrgs(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to list orgs")
		return
	}
	items := make([]orgResponse, len(orgs))
	for i := range orgs {
		items[i] = orgToResponse(orgs[i])
	}
	respondJSON(w, http.StatusOK, listResponse[orgResponse]{Items: items, Total: len(items)})
}

// CreateOrg creates an organization.
func (h *AdminHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	var req orgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.DisplayName == "" || req.AdminGroupID == "" {
		respondError(w, http.StatusBadRequest, "validation_error", "name, display_name, admin_group_id required")
		return
	}
	o, err := h.store.Queries.CreateOrg(r.Context(), sqlc.CreateOrgParams{
		Name:          req.Name,
		DisplayName:   req.DisplayName,
		AdminGroupID:  req.AdminGroupID,
		ReaderGroupID: pgtype.Text{String: req.ReaderGroupID, Valid: req.ReaderGroupID != ""},
	})
	if err != nil {
		if isUniqueViolation(err) {
			respondError(w, http.StatusConflict, "conflict", "org name already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, "internal", "failed to create org")
		return
	}
	auditLog(r.Context(), h.store, actorFromCtx(r.Context()), o.ID, "org.create", "org", o.ID.String())
	respondJSON(w, http.StatusCreated, orgToResponse(o))
}

// UpdateOrg updates an organization.
func (h *AdminHandler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "org")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return
	}
	var req orgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	o, err := h.store.Queries.UpdateOrg(r.Context(), sqlc.UpdateOrgParams{
		ID:            id,
		DisplayName:   req.DisplayName,
		AdminGroupID:  req.AdminGroupID,
		ReaderGroupID: pgtype.Text{String: req.ReaderGroupID, Valid: req.ReaderGroupID != ""},
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to update org")
		return
	}
	respondJSON(w, http.StatusOK, orgToResponse(o))
}

// DeleteOrg deletes an organization.
func (h *AdminHandler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "org")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return
	}
	var clusterCount, pipelineCount int
	// RAW-SQL-OK: cross-table count with two columns — no sqlc equivalent
	err := h.store.Pool().QueryRow(r.Context(),
		`SELECT
			(SELECT count(*) FROM clusters WHERE org_id = $1)::int,
			(SELECT count(*) FROM pipelines WHERE org_id = $1)::int`,
		id).Scan(&clusterCount, &pipelineCount)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to check org content")
		return
	}
	if clusterCount > 0 || pipelineCount > 0 {
		msg := fmt.Sprintf("org has %d clusters, %d pipelines", clusterCount, pipelineCount)
		respondError(w, http.StatusConflict, "not_empty", msg)
		return
	}

	if err := h.store.Queries.DeleteOrg(r.Context(), id); err != nil {
		if isFKViolation(err) {
			respondError(w, http.StatusConflict, "not_empty", "org still has references")
			return
		}
		respondError(w, http.StatusInternalServerError, "internal", "failed to delete org")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type clusterResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OrgID     string `json:"org_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

func clusterToResponse(c sqlc.Cluster) clusterResponse {
	var id, orgID string
	id = c.ID.String()
	orgID = c.OrgID.String()
	return clusterResponse{ID: id, Name: c.Name, OrgID: orgID, CreatedAt: c.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z")}
}

// ListClusters lists clusters.
func (h *AdminHandler) ListClusters(w http.ResponseWriter, r *http.Request) {
	unclaimed := r.URL.Query().Get("unclaimed") == "true"
	if unclaimed {
		clusters, _ := h.store.Queries.ListUnclaimedClusters(r.Context()) //nolint:errcheck // empty list is safe fallback
		items := make([]clusterResponse, len(clusters))
		for i := range clusters {
			items[i] = clusterToResponse(clusters[i])
		}
		respondJSON(w, http.StatusOK, listResponse[clusterResponse]{Items: items, Total: len(items)})
		return
	}
	clusters, err := h.store.Queries.ListAllClusters(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to list clusters")
		return
	}
	items := make([]clusterResponse, len(clusters))
	for i := range clusters {
		items[i] = clusterToResponse(clusters[i])
	}
	respondJSON(w, http.StatusOK, listResponse[clusterResponse]{Items: items, Total: len(items)})
}

// ClaimCluster assigns a cluster to an organization.
func (h *AdminHandler) ClaimCluster(w http.ResponseWriter, r *http.Request) {
	cluster, err := h.store.Queries.GetClusterByName(r.Context(), chi.URLParam(r, "cluster"))
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}
	var body struct {
		OrgID string `json:"org_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var orgID pgtype.UUID
	if err := orgID.Scan(body.OrgID); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org_id")
		return
	}
	if err := h.store.Queries.ClaimCluster(r.Context(), sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID}); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to claim cluster")
		return
	}
	h.logger.Info("cluster claimed", "cluster_id", cluster.ID, "org_id", orgID)
	respondJSON(w, http.StatusOK, map[string]string{"status": "claimed"})
}

// UnclaimCluster removes a cluster's organization assignment.
func (h *AdminHandler) UnclaimCluster(w http.ResponseWriter, r *http.Request) {
	cluster, err := h.store.Queries.GetClusterByName(r.Context(), chi.URLParam(r, "cluster"))
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}
	if err := h.store.Queries.UnclaimCluster(r.Context(), cluster.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to unclaim cluster")
		return
	}
	// RAW-SQL-OK: cluster-scoped dirty marking — no sqlc query covers this join shape
	if _, err := h.store.Pool().Exec(r.Context(),
		`UPDATE serve_cache sc SET dirty = true
		 FROM collectors c WHERE sc.collector_id = c.id AND c.cluster_id = $1`,
		cluster.ID); err != nil {
		h.logger.Warn("unclaim: failed to mark serve_cache dirty", "cluster_id", cluster.ID, "err", err)
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "unclaimed"})
}

type tokenResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// ListTokens lists agent tokens.
func (h *AdminHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, _ := h.store.Queries.ListAgentTokens(r.Context()) //nolint:errcheck // empty list is safe fallback
	items := make([]tokenResponse, len(tokens))
	for i := range tokens {
		t := tokens[i]
		status := "active"
		if t.RevokedAt.Valid {
			status = "revoked"
		}
		id := t.ID.String()
		items[i] = tokenResponse{ID: id, Name: t.Name, CreatedBy: t.CreatedBy, Status: status, CreatedAt: t.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z")}
	}
	respondJSON(w, http.StatusOK, listResponse[tokenResponse]{Items: items, Total: len(items)})
}

// CreateToken creates an agent token.
func (h *AdminHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		respondError(w, http.StatusBadRequest, "validation_error", "name required")
		return
	}
	actor := actorFromCtx(r.Context())
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to generate secret")
		return
	}
	secret := base64.URLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))
	tok, err := h.store.Queries.CreateAgentToken(r.Context(), sqlc.CreateAgentTokenParams{
		Name: body.Name, TokenHash: hash[:], CreatedBy: actor,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to create token")
		return
	}
	id := tok.ID.String()
	respondJSON(w, http.StatusCreated, map[string]string{
		"id":     id,
		"name":   tok.Name,
		"secret": secret,
	})
}

// RevokeToken revokes an agent token.
func (h *AdminHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid token id")
		return
	}
	if err := h.store.Queries.RevokeAgentToken(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to revoke token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SearchGroups searches groups.
func (h *AdminHandler) SearchGroups(w http.ResponseWriter, r *http.Request) {
	// Graph-backed group search — implemented in M5 when Graph client is wired.
	respondJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0})
}

// Ensure json is used.
var _ = json.Marshal
