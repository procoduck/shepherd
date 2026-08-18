package mgmtapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// RepoLinksHandler handles /api/orgs/{org}/ado-credentials and /api/orgs/{org}/repo-links.
type RepoLinksHandler struct {
	store  *store.Store
	crypto *crypto.Encryptor
	logger *slog.Logger
}

// NewRepoLinksHandler creates a repository links handler.
func NewRepoLinksHandler(st *store.Store, enc *crypto.Encryptor, logger *slog.Logger) *RepoLinksHandler {
	return &RepoLinksHandler{store: st, crypto: enc, logger: logger}
}

// -- ADO Credentials --

type adoCredRequest struct {
	Name          string `json:"name"`
	ADOOrgURL     string `json:"ado_org_url"`
	EntraTenantID string `json:"entra_tenant_id"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
}

type adoCredResponse struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ADOOrgURL     string `json:"ado_org_url"`
	EntraTenantID string `json:"entra_tenant_id"`
	ClientID      string `json:"client_id"`
	CreatedAt     string `json:"created_at"`
}

// ListCredentials returns ADO credentials for the organization.
func (h *RepoLinksHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	rows, _ := h.store.Queries.ListADOCredentialsByOrg(r.Context(), orgID) //nolint:errcheck // empty is safe
	items := make([]adoCredResponse, len(rows))
	for i := range rows {
		c := rows[i]
		id := c.ID.String()
		items[i] = adoCredResponse{ID: id, Name: c.Name, ADOOrgURL: c.AdoOrgUrl, EntraTenantID: c.EntraTenantID, ClientID: c.ClientID, CreatedAt: c.CreatedAt.Time.Format("2006-01-02T15:04:05Z")}
	}
	respondJSON(w, http.StatusOK, listResponse[adoCredResponse]{Items: items, Total: len(items)})
}

// CreateCredential creates an ADO credential.
func (h *RepoLinksHandler) CreateCredential(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	var req adoCredRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ClientSecret == "" {
		respondError(w, http.StatusBadRequest, "validation_error", "client_secret required")
		return
	}
	enc, err := h.crypto.Encrypt([]byte(req.ClientSecret))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "encryption failed")
		return
	}
	c, err := h.store.Queries.CreateADOCredential(r.Context(), sqlc.CreateADOCredentialParams{
		OrgID: orgID, Name: req.Name, AdoOrgUrl: req.ADOOrgURL,
		EntraTenantID: req.EntraTenantID, ClientID: req.ClientID, ClientSecretEnc: enc,
	})
	if err != nil {
		if isUniqueViolation(err) {
			respondError(w, http.StatusConflict, "conflict", "credential name already exists")
			return
		}
		respondError(w, http.StatusInternalServerError, "internal", "failed to create credential")
		return
	}
	id := c.ID.String()
	respondJSON(w, http.StatusCreated, adoCredResponse{ID: id, Name: c.Name, ADOOrgURL: c.AdoOrgUrl, EntraTenantID: c.EntraTenantID, ClientID: c.ClientID, CreatedAt: c.CreatedAt.Time.Format("2006-01-02T15:04:05Z")})
}

// DeleteCredential deletes an ADO credential.
func (h *RepoLinksHandler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	_ = h.store.Queries.DeleteADOCredential(r.Context(), id) //nolint:errcheck // empty list is safe fallback
	w.WriteHeader(http.StatusNoContent)
}

// -- Repo Links --

type repoLinkRequest struct {
	CollectorID         string `json:"collector_id"`
	CredentialID        string `json:"credential_id"`
	Project             string `json:"project"`
	Repository          string `json:"repository"`
	Branch              string `json:"branch"`
	Path                string `json:"path"`
	PollIntervalSeconds int32  `json:"poll_interval_seconds"`
}

type repoLinkResponse struct {
	ID           string `json:"id"`
	Project      string `json:"project"`
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
	SyncStatus   string `json:"sync_status,omitempty"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
}

// ListRepoLinks returns repository links for the organization.
func (h *RepoLinksHandler) ListRepoLinks(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	rows, _ := h.store.Queries.ListRepoLinksByOrg(r.Context(), orgID) //nolint:errcheck // empty is safe
	items := make([]repoLinkResponse, len(rows))
	for i := range rows {
		l := rows[i]
		id := l.ID.String()
		items[i] = repoLinkResponse{ID: id, Project: l.Project, Repository: l.Repository, Branch: l.Branch, Path: l.Path, SyncStatus: l.SyncStatus.String, LastSyncedAt: l.LastSyncedAt.Time.Format("2006-01-02T15:04:05Z")}
	}
	respondJSON(w, http.StatusOK, listResponse[repoLinkResponse]{Items: items, Total: len(items)})
}

// CreateRepoLink creates a repository link.
func (h *RepoLinksHandler) CreateRepoLink(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	var req repoLinkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var collID, credID pgtype.UUID
	if err := collID.Scan(req.CollectorID); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid collector_id")
		return
	}
	if err := credID.Scan(req.CredentialID); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid credential_id")
		return
	}
	branch := req.Branch
	if branch == "" {
		branch = "main"
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	poll := req.PollIntervalSeconds
	if poll == 0 {
		poll = 180
	}
	l, err := h.store.Queries.CreateRepoLink(r.Context(), sqlc.CreateRepoLinkParams{
		OrgID: orgID, CollectorID: collID, CredentialID: credID,
		Project: req.Project, Repository: req.Repository, Branch: branch, Path: path, PollIntervalSeconds: poll,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to create repo link")
		return
	}
	id := l.ID.String()
	respondJSON(w, http.StatusCreated, repoLinkResponse{ID: id, Project: l.Project, Repository: l.Repository, Branch: l.Branch, Path: l.Path})
}

// DeleteRepoLink deletes a repository link.
func (h *RepoLinksHandler) DeleteRepoLink(w http.ResponseWriter, r *http.Request) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid id")
		return
	}
	_ = h.store.Queries.DeleteRepoLink(r.Context(), id) //nolint:errcheck // empty list is safe fallback
	w.WriteHeader(http.StatusNoContent)
}
