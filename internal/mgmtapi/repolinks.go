package mgmtapi

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/proto"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// RepoLinksHandler is a thin REST shim over GitOpsService for
// /api/orgs/{org}/ado-credentials and /api/orgs/{org}/repo-links: it parses
// URL params/query/body into the shepherd.mgmt.v1 proto request, calls the
// service method directly (in-process, not over HTTP), and renders the
// response with the shim helpers in shim.go so the legacy JSON stays
// byte-compatible. No business logic lives here — see rpc_gitops.go.
type RepoLinksHandler struct {
	svc *GitOpsService
}

// NewRepoLinksHandler creates a repository links REST shim backed by svc.
func NewRepoLinksHandler(svc *GitOpsService) *RepoLinksHandler {
	return &RepoLinksHandler{svc: svc}
}

// writeProtoJSON renders msg as legacy-compatible JSON (see MarshalOpts in
// shim.go) with the given HTTP status.
func writeProtoJSON(w http.ResponseWriter, status int, msg proto.Message) {
	b, err := MarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", "failed to marshal response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:errcheck // response status/headers already committed
}

// -- ADO Credentials --

// adoCredRequest is the legacy wire shape for POST .../ado-credentials.
type adoCredRequest struct {
	Name          string `json:"name"`
	ADOOrgURL     string `json:"ado_org_url"`
	EntraTenantID string `json:"entra_tenant_id"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
}

// ListCredentials returns ADO credentials for the organization.
func (h *RepoLinksHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListCredentialsRequest{OrgId: chi.URLParam(r, "org")}
	resp, err := h.svc.ListCredentials(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// CreateCredential creates an ADO credential.
func (h *RepoLinksHandler) CreateCredential(w http.ResponseWriter, r *http.Request) {
	var body adoCredRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.CreateCredentialRequest{
		OrgId:         chi.URLParam(r, "org"),
		Name:          body.Name,
		AdoOrgUrl:     body.ADOOrgURL,
		EntraTenantId: body.EntraTenantID,
		ClientId:      body.ClientID,
		ClientSecret:  body.ClientSecret,
	}
	resp, err := h.svc.CreateCredential(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusCreated, resp.Msg)
}

// DeleteCredential deletes an ADO credential.
func (h *RepoLinksHandler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.DeleteCredentialRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	if _, err := h.svc.DeleteCredential(r.Context(), connect.NewRequest(req)); err != nil {
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// repoLinkOmitFields names the RepoLink fields the legacy repoLinkResponse
// struct marked `,omitempty`: both are unset on a just-created repo link
// (its poller hasn't run yet).
var repoLinkOmitFields = []string{"sync_status", "last_synced_at"} //nolint:gochecknoglobals // shared, read-only field list

// -- Repo Links --

// repoLinkRequest is the legacy wire shape for POST .../repo-links.
type repoLinkRequest struct {
	CollectorID         string `json:"collector_id"`
	CredentialID        string `json:"credential_id"`
	Project             string `json:"project"`
	Repository          string `json:"repository"`
	Branch              string `json:"branch"`
	Path                string `json:"path"`
	PollIntervalSeconds int32  `json:"poll_interval_seconds"`
}

// ListRepoLinks returns repository links for the organization.
func (h *RepoLinksHandler) ListRepoLinks(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListRepoLinksRequest{OrgId: chi.URLParam(r, "org")}
	resp, err := h.svc.ListRepoLinks(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, repoLinkOmitFields...)
}

// CreateRepoLink creates a repository link.
func (h *RepoLinksHandler) CreateRepoLink(w http.ResponseWriter, r *http.Request) {
	var body repoLinkRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.CreateRepoLinkRequest{
		OrgId:               chi.URLParam(r, "org"),
		CollectorId:         body.CollectorID,
		CredentialId:        body.CredentialID,
		Project:             body.Project,
		Repository:          body.Repository,
		Branch:              body.Branch,
		Path:                body.Path,
		PollIntervalSeconds: body.PollIntervalSeconds,
	}
	resp, err := h.svc.CreateRepoLink(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusCreated, resp.Msg, repoLinkOmitFields...)
}

// DeleteRepoLink deletes a repository link.
func (h *RepoLinksHandler) DeleteRepoLink(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.DeleteRepoLinkRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	if _, err := h.svc.DeleteRepoLink(r.Context(), connect.NewRequest(req)); err != nil {
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
