package mgmtapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/auth"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// OrgsHandler handles org-scoped routes.
type OrgsHandler struct {
	store  *store.Store
	logger *slog.Logger
}

// NewOrgsHandler creates an organization handler.
func NewOrgsHandler(st *store.Store, logger *slog.Logger) *OrgsHandler {
	return &OrgsHandler{store: st, logger: logger}
}

type meOrgEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type meResponse struct {
	UserOID     string       `json:"user_oid"`
	Email       string       `json:"email"`
	DisplayName string       `json:"display_name"`
	IsAppAdmin  bool         `json:"is_app_admin"`
	AuthMethod  string       `json:"auth_method"`
	Orgs        []meOrgEntry `json:"orgs"`
}

// Me returns the current actor and organizations.
func (h *OrgsHandler) Me(w http.ResponseWriter, r *http.Request) {
	// Never cache /api/me — authentication state must always be fresh.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	sess := auth.SessionFromCtx(r.Context())
	if sess == nil {
		respondError(w, http.StatusUnauthorized, "unauthenticated", "not authenticated")
		return
	}
	authMethod := "oidc"
	if sess.Source == "local" || sess.Source == "dev" {
		authMethod = sess.Source
	}
	allOrgs, err := h.store.Queries.ListOrgs(r.Context())
	if err != nil {
		h.logger.Error("me: listing orgs", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to list orgs")
		return
	}
	var orgEntries []meOrgEntry
	for i := range allOrgs {
		org := &allOrgs[i]
		if sess.IsAppAdmin {
			orgEntries = append(orgEntries, meOrgEntry{ID: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: "admin"})
			continue
		}
		role := ""
		if slices.Contains(sess.GroupIDs, org.AdminGroupID) {
			role = "admin"
		} else if org.ReaderGroupID.Valid && slices.Contains(sess.GroupIDs, org.ReaderGroupID.String) {
			role = "reader"
		}
		if role != "" {
			orgEntries = append(orgEntries, meOrgEntry{ID: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: role})
		}
	}
	if orgEntries == nil {
		orgEntries = []meOrgEntry{}
	}
	respondJSON(w, http.StatusOK, meResponse{UserOID: sess.UserOID, Email: sess.Email, DisplayName: sess.DisplayName, IsAppAdmin: sess.IsAppAdmin, AuthMethod: authMethod, Orgs: orgEntries})
}

type collectorResponse struct {
	ID                 string                      `json:"id"`
	ClusterID          string                      `json:"cluster_id"`
	Cluster            string                      `json:"cluster"`
	Role               string                      `json:"role"`
	OrgID              string                      `json:"org_id"`
	RemoteConfigStatus string                      `json:"remote_config_status,omitempty"`
	RemoteConfigError  string                      `json:"remote_config_error,omitempty"`
	LastSeen           string                      `json:"last_seen,omitempty"`
	AlloyVersion       string                      `json:"alloy_version,omitempty"`
	LocalAttributes    json.RawMessage             `json:"local_attributes,omitempty"`
	Instances          []collectorInstanceResponse `json:"instances,omitempty"`
}

type collectorInstanceResponse struct {
	Name               string          `json:"name"`
	AlloyVersion       string          `json:"alloy_version,omitempty"`
	OS                 string          `json:"os,omitempty"`
	LastSeen           string          `json:"last_seen,omitempty"`
	RemoteConfigStatus string          `json:"remote_config_status,omitempty"`
	RemoteConfigError  string          `json:"remote_config_error,omitempty"`
	LocalAttributes    json.RawMessage `json:"local_attributes,omitempty"`
}

// timestampOrEmpty formats a nullable timestamptz as RFC3339, or "" when unset.
func timestampOrEmpty(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format("2006-01-02T15:04:05Z")
}

// ListCollectors returns collectors for the organization.
// App admins receive all collectors across all orgs (with org_id included in each item).
func (h *OrgsHandler) ListCollectors(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	if !orgID.Valid {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid org id")
		return
	}

	sess := auth.SessionFromCtx(r.Context())
	if sess != nil && sess.IsAppAdmin {
		all, err := h.store.Queries.ListAllCollectors(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, "internal", "failed to list collectors")
			return
		}
		items := make([]collectorResponse, len(all))
		for i := range all {
			c := &all[i]
			summary, _ := h.store.Queries.GetLatestCollectorInstanceSummary(r.Context(), c.ID) //nolint:errcheck // zero value is safe default
			items[i] = collectorResponse{
				ID:                 c.ID.String(),
				ClusterID:          c.ClusterID.String(),
				Cluster:            c.ClusterName,
				Role:               c.Role,
				OrgID:              c.OrgID.String(),
				RemoteConfigStatus: summary.RemoteConfigStatus.String,
				LastSeen:           timestampOrEmpty(summary.LastSeen),
				AlloyVersion:       summary.AlloyVersion.String,
			}
		}
		respondJSON(w, http.StatusOK, listResponse[collectorResponse]{Items: items, Total: len(items)})
		return
	}

	collectors, err := h.store.Queries.ListCollectorsByOrg(r.Context(), orgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to list collectors")
		return
	}
	items := make([]collectorResponse, len(collectors))
	for i, c := range collectors {
		var id, clusterID string
		id = c.ID.String()
		clusterID = c.ClusterID.String()
		cluster, _ := h.store.Queries.GetClusterByID(r.Context(), c.ClusterID)             //nolint:errcheck // empty name is safe
		summary, _ := h.store.Queries.GetLatestCollectorInstanceSummary(r.Context(), c.ID) //nolint:errcheck // zero value is safe default
		items[i] = collectorResponse{
			ID:                 id,
			ClusterID:          clusterID,
			Cluster:            cluster.Name,
			Role:               c.Role,
			RemoteConfigStatus: summary.RemoteConfigStatus.String,
			LastSeen:           timestampOrEmpty(summary.LastSeen),
			AlloyVersion:       summary.AlloyVersion.String,
		}
	}
	respondJSON(w, http.StatusOK, listResponse[collectorResponse]{Items: items, Total: len(items)})
}

// GetCollector returns a collector, including its live instances.
func (h *OrgsHandler) GetCollector(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid collector id")
		return
	}
	c, err := h.store.Queries.GetCollectorByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", "collector not found")
		return
	}
	var cid, clusterID string
	cid = c.ID.String()
	clusterID = c.ClusterID.String()
	cluster, _ := h.store.Queries.GetClusterByID(r.Context(), c.ClusterID) //nolint:errcheck // empty name is safe

	rows, err := h.store.Queries.ListCollectorInstancesByCollector(r.Context(), id)
	if err != nil {
		h.logger.Warn("get collector: listing instances", "err", err)
		rows = nil
	}
	instances := make([]collectorInstanceResponse, len(rows))
	for i := range rows {
		row := &rows[i]
		instances[i] = collectorInstanceResponse{
			Name:               row.Name,
			AlloyVersion:       row.AlloyVersion.String,
			OS:                 row.Os.String,
			LastSeen:           timestampOrEmpty(row.LastSeen),
			RemoteConfigStatus: row.RemoteConfigStatus.String,
			RemoteConfigError:  row.RemoteConfigError.String,
			LocalAttributes:    row.LocalAttributes,
		}
	}
	resp := collectorResponse{ID: cid, ClusterID: clusterID, Cluster: cluster.Name, Role: c.Role, Instances: instances}
	if len(instances) > 0 {
		latest := instances[0]
		resp.RemoteConfigStatus = latest.RemoteConfigStatus
		resp.RemoteConfigError = latest.RemoteConfigError
		resp.LastSeen = latest.LastSeen
		resp.AlloyVersion = latest.AlloyVersion
		resp.LocalAttributes = latest.LocalAttributes
	}
	respondJSON(w, http.StatusOK, resp)
}

// ServedConfig returns the served configuration for a collector.
func (h *OrgsHandler) ServedConfig(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid collector id")
		return
	}
	cache, err := h.store.Queries.GetServeCache(r.Context(), id)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]string{"content": "", "hash": ""})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"content":     cache.Content,
		"hash":        cache.Hash,
		"computed_at": cache.ComputedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// CreateAssignment creates a group assignment.
func (h *OrgsHandler) CreateAssignment(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid collector id")
		return
	}
	var body struct {
		GroupID          string `json:"group_id"`
		GroupDisplayName string `json:"group_display_name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	a, err := h.store.Queries.CreateGroupAssignment(r.Context(), sqlc.CreateGroupAssignmentParams{
		CollectorID:      id,
		GroupID:          body.GroupID,
		GroupDisplayName: body.GroupDisplayName,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to create assignment")
		return
	}
	aid := a.ID.String()
	respondJSON(w, http.StatusCreated, map[string]string{"id": aid, "group_id": a.GroupID})
}

// DeleteAssignment deletes a group assignment.
func (h *OrgsHandler) DeleteAssignment(w http.ResponseWriter, r *http.Request) {
	var collID pgtype.UUID
	if err := collID.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid collector id")
		return
	}
	if err := h.store.Queries.DeleteGroupAssignment(r.Context(), sqlc.DeleteGroupAssignmentParams{
		CollectorID: collID,
		GroupID:     chi.URLParam(r, "group_id"),
	}); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to delete assignment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListAttributes returns organization attributes.
func (h *OrgsHandler) ListAttributes(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	keys, err := h.store.Queries.ListDistinctAttributeKeys(r.Context(), orgID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to list attributes")
		return
	}
	result := map[string][]string{"cluster": {}, "role": {}}
	for _, k := range keys {
		vals, _ := h.store.Queries.ListDistinctAttributeValues(r.Context(), sqlc.ListDistinctAttributeValuesParams{ //nolint:errcheck // empty is safe fallback
			OrgID:   orgID,
			Column2: k,
		})
		if vals == nil {
			vals = []string{}
		}
		result[k] = vals
	}
	respondJSON(w, http.StatusOK, result)
}

// Destination handlers.

type destinationResponse struct {
	ID              string          `json:"id"`
	OrgID           string          `json:"org_id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	URL             string          `json:"url"`
	TenantID        string          `json:"tenant_id"`
	SecretName      string          `json:"secret_name"`
	SecretNamespace string          `json:"secret_namespace"`
	AuthMode        string          `json:"auth_mode"`
	Extra           json.RawMessage `json:"extra,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func destToResponse(d sqlc.Destination) destinationResponse {
	var id, orgID string
	id = d.ID.String()
	orgID = d.OrgID.String()
	return destinationResponse{
		ID: id, OrgID: orgID, Name: d.Name, Type: d.Type, URL: d.Url,
		TenantID: d.TenantID, SecretName: d.SecretName, SecretNamespace: d.SecretNamespace,
		AuthMode: d.AuthMode, Extra: d.Extra,
		CreatedAt: d.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt: d.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

type destinationRequest struct {
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	URL             string          `json:"url"`
	TenantID        string          `json:"tenant_id"`
	SecretName      string          `json:"secret_name"`
	SecretNamespace string          `json:"secret_namespace"`
	AuthMode        string          `json:"auth_mode"`
	Extra           json.RawMessage `json:"extra"`
}

// ListDestinations returns destinations for the organization.
func (h *OrgsHandler) ListDestinations(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	dests, _ := h.store.Queries.ListDestinationsByOrg(r.Context(), orgID) //nolint:errcheck // empty is safe fallback
	items := make([]destinationResponse, len(dests))
	for i := range dests {
		d := dests[i]
		items[i] = destToResponse(d)
	}
	respondJSON(w, http.StatusOK, listResponse[destinationResponse]{Items: items, Total: len(items)})
}

// CreateDestination creates a destination.
func (h *OrgsHandler) CreateDestination(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	var req destinationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	extra := req.Extra
	if len(extra) == 0 {
		extra = json.RawMessage("{}")
	}
	d, err := h.store.Queries.CreateDestination(r.Context(), sqlc.CreateDestinationParams{
		OrgID: orgID, Name: req.Name, Type: req.Type, Url: req.URL,
		TenantID: req.TenantID, SecretName: req.SecretName, SecretNamespace: req.SecretNamespace,
		AuthMode: req.AuthMode, Extra: extra,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respondError(w, http.StatusConflict, "conflict", "destination name already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, "internal", "failed to create destination")
		return
	}
	respondJSON(w, http.StatusCreated, destToResponse(d))
}

// GetDestination returns a destination.
func (h *OrgsHandler) GetDestination(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid destination id")
		return
	}
	d, err := h.store.Queries.GetDestinationByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "not_found", "destination not found")
		return
	}
	respondJSON(w, http.StatusOK, destToResponse(d))
}

// UpdateDestination updates a destination.
func (h *OrgsHandler) UpdateDestination(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid destination id")
		return
	}
	var req destinationRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	extra := req.Extra
	if len(extra) == 0 {
		extra = json.RawMessage("{}")
	}
	d, err := h.store.Queries.UpdateDestination(r.Context(), sqlc.UpdateDestinationParams{
		ID: id, Name: req.Name, Type: req.Type, Url: req.URL,
		TenantID: req.TenantID, SecretName: req.SecretName, SecretNamespace: req.SecretNamespace,
		AuthMode: req.AuthMode, Extra: extra,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to update destination")
		return
	}
	respondJSON(w, http.StatusOK, destToResponse(d))
}

// DeleteDestination deletes a destination.
func (h *OrgsHandler) DeleteDestination(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid destination id")
		return
	}
	// RAW-SQL-OK: JSONB containment check on wizard_state — no sqlc equivalent
	rows, err := h.store.Pool().Query(r.Context(),
		`SELECT name FROM pipelines
		 WHERE wizard_state IS NOT NULL
		 AND wizard_state @> jsonb_build_object('destination_id', $1::text)
		 ORDER BY name`,
		id.String())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to check destination references")
		return
	}
	var refNames []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr == nil {
			refNames = append(refNames, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to scan references")
		return
	}
	if len(refNames) > 0 {
		msg := fmt.Sprintf("referenced by %d wizard pipeline(s): %s", len(refNames), strings.Join(refNames, ", "))
		respondError(w, http.StatusConflict, "in_use", msg)
		return
	}
	if err := h.store.Queries.DeleteDestination(r.Context(), id); err != nil {
		h.logger.Warn("delete destination", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to delete destination")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
