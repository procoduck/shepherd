package mgmtapi

import (
	"errors"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/store"
)

// AdminHandler is a thin REST shim over AdminService for /api/admin routes:
// it parses URL params/query/body into the shepherd.mgmt.v1 proto request,
// calls the service method directly (in-process, not over HTTP), and
// renders the response with the shim helpers in shim.go so the legacy JSON
// stays byte-compatible. No business logic lives here — see rpc_admin.go.
type AdminHandler struct {
	svc *AdminService
}

// NewAdminHandler creates an admin route handler backed by an AdminService
// constructed from the same deps the legacy handler used.
func NewAdminHandler(st *store.Store, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{svc: NewAdminService(st, logger)}
}

// orgRequest is the legacy wire shape for POST/PATCH .../admin/orgs.
type orgRequest struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	AdminGroupID  string `json:"admin_group_id"`
	ReaderGroupID string `json:"reader_group_id"`
	EditorGroupID string `json:"editor_group_id"`
}

// orgOmitFields names the Org field(s) omitted from the legacy JSON when
// empty — the fields the legacy orgResponse struct marked `,omitempty`, plus
// editor_group_id, which postdates that struct and is omitted for the same
// reason: an org with no editor tier should not advertise an empty one.
// Every other Org field is always emitted, even when empty.
var orgOmitFields = []string{"reader_group_id", "editor_group_id"} //nolint:gochecknoglobals // shared, read-only field list

// ListOrgs lists organizations.
func (h *AdminHandler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.ListOrgs(r.Context(), connect.NewRequest(&mgmtv1.ListOrgsRequest{}))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, orgOmitFields...)
}

// CreateOrg creates an organization.
func (h *AdminHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	var body orgRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.CreateOrgRequest{
		Name: body.Name, DisplayName: body.DisplayName,
		AdminGroupId: body.AdminGroupID, ReaderGroupId: body.ReaderGroupID,
		EditorGroupId: body.EditorGroupID,
	}
	resp, err := h.svc.CreateOrg(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusCreated, resp.Msg, orgOmitFields...)
}

// UpdateOrg updates an organization.
func (h *AdminHandler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	var body orgRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.UpdateOrgRequest{
		OrgId: chi.URLParam(r, "org"), DisplayName: body.DisplayName,
		AdminGroupId: body.AdminGroupID, ReaderGroupId: body.ReaderGroupID,
		EditorGroupId: body.EditorGroupID,
	}
	resp, err := h.svc.UpdateOrg(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, orgOmitFields...)
}

// DeleteOrg deletes an organization. A non-empty org (orgNotEmptyError from
// rpc_admin.go) renders the legacy {"error":{"code":"not_empty",...}}
// envelope exactly (see rpc_admin.go's orgNotEmptyError doc comment); every
// other error uses the generic connect error shim.
func (h *AdminHandler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.DeleteOrgRequest{OrgId: chi.URLParam(r, "org")}
	if _, err := h.svc.DeleteOrg(r.Context(), connect.NewRequest(req)); err != nil {
		var notEmpty *orgNotEmptyError
		if errors.As(err, &notEmpty) {
			respondError(w, http.StatusConflict, "not_empty", notEmpty.Error())
			return
		}
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListClusters lists clusters, optionally filtered to unclaimed ones via
// ?unclaimed=true.
func (h *AdminHandler) ListClusters(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListClustersRequest{Unclaimed: r.URL.Query().Get("unclaimed") == "true"}
	resp, err := h.svc.ListClusters(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// org_id is the legacy clusterResponse struct's one omitempty field —
	// omitted for every unclaimed cluster.
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, "org_id")
}

// claimClusterRequest is the legacy wire shape for POST .../clusters/{cluster}/claim.
type claimClusterRequest struct {
	OrgID string `json:"org_id"`
}

// ClaimCluster assigns a cluster to an organization.
func (h *AdminHandler) ClaimCluster(w http.ResponseWriter, r *http.Request) {
	var body claimClusterRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.ClaimClusterRequest{Cluster: chi.URLParam(r, "cluster"), OrgId: body.OrgID}
	resp, err := h.svc.ClaimCluster(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// UnclaimCluster removes a cluster's organization assignment.
func (h *AdminHandler) UnclaimCluster(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.UnclaimClusterRequest{Cluster: chi.URLParam(r, "cluster")}
	resp, err := h.svc.UnclaimCluster(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// ListTokens lists agent tokens.
func (h *AdminHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.ListAgentTokens(r.Context(), connect.NewRequest(&mgmtv1.ListAgentTokensRequest{}))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// createTokenRequest is the legacy wire shape for POST .../admin/agent-tokens.
type createTokenRequest struct {
	Name string `json:"name"`
}

// CreateToken creates an agent token. The response includes the plaintext
// secret exactly once — it is never logged.
func (h *AdminHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var body createTokenRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.CreateAgentTokenRequest{Name: body.Name}
	resp, err := h.svc.CreateAgentToken(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusCreated, resp.Msg)
}

// RevokeToken revokes an agent token.
func (h *AdminHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.RevokeAgentTokenRequest{Id: chi.URLParam(r, "id")}
	if _, err := h.svc.RevokeAgentToken(r.Context(), connect.NewRequest(req)); err != nil {
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SearchGroups searches groups. org_id is optional — it scopes the search
// (and, per requireAppOrOrgAdmin in router.go, the authorization check) to
// one org for an org-admin caller; an app admin may omit it.
func (h *AdminHandler) SearchGroups(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.SearchGroupsRequest{OrgId: r.URL.Query().Get("org_id"), Q: r.URL.Query().Get("q")}
	resp, err := h.svc.SearchGroups(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}
