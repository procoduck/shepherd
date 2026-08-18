package mgmtapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/store"
)

// AuditHandler is a thin REST shim over AuditService for
// /api/orgs/{org}/audit: it parses URL params/query into the
// shepherd.mgmt.v1 proto request, calls the service method directly
// (in-process, not over HTTP), and renders the response with the shim
// helpers so the legacy JSON stays byte-compatible. No business logic lives
// here — see rpc_audit.go.
type AuditHandler struct {
	svc *AuditService
}

// NewAuditHandler creates an audit REST shim backed by a fresh AuditService.
func NewAuditHandler(st *store.Store, logger *slog.Logger) *AuditHandler {
	return &AuditHandler{svc: NewAuditService(st, logger)}
}

// auditQueryInt32 parses a query param as int32, returning 0 (which
// AuditService.ListAudit treats as "unset", applying its own default) for
// an absent or non-numeric value — mirroring the parse-failure branch of the
// legacy paginationParams helper this shim replaces.
func auditQueryInt32(r *http.Request, name string) int32 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0
	}
	return int32(n)
}

// List returns audit log entries (thin REST shim over
// AuditService.ListAudit).
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	req := connect.NewRequest(&mgmtv1.ListAuditRequest{
		OrgId:  chi.URLParam(r, "org"),
		Limit:  auditQueryInt32(r, "limit"),
		Offset: auditQueryInt32(r, "offset"),
		Actor:  r.URL.Query().Get("actor"),
		Action: r.URL.Query().Get("action"),
	})
	resp, err := h.svc.ListAudit(r.Context(), req)
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// legacy auditResponse always emits every field except org_id (its one
	// omitempty field) — writeProtoJSONOmit drops it when empty while still
	// keeping "items"/"total" present when the list itself is empty,
	// matching the legacy listResponse envelope exactly.
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, "org_id")
}
