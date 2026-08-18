package mgmtapi

import (
	"io"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/validate"
)

// upgradeCheckMaxBodyBytes caps upgrade-check request bodies at 512 KiB.
const upgradeCheckMaxBodyBytes = 512 * 1024

// VisualHandler handles /api/orgs/{org}/visual/* and the pipeline graph-view
// route — a thin shim over VisualService (rpc_visual.go). It parses URL
// params/query/body into the proto request, calls the service directly
// in-process, and renders the response as legacy-compatible JSON. No business
// logic lives here.
type VisualHandler struct {
	service *VisualService
	logger  *slog.Logger
}

func NewVisualHandler(st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger) *VisualHandler { //nolint:revive
	return &VisualHandler{service: NewVisualService(st, v, reg, logger), logger: logger}
}

// Render handles POST /api/orgs/{org}/visual/render.
func (h *VisualHandler) Render(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.RenderRequest{OrgId: chi.URLParam(r, "org")}
	if !visualDecodeBody(w, r, 0, req) {
		return
	}
	resp, err := h.service.Render(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// Legacy shape: a non-empty Diagnostics list from the render step is a
	// 422 wrapped in {"error":{"code":"render_error"},"diagnostics":[...]},
	// not the normal {"content","node_map","diagnostics"} 200 body.
	if diags := resp.Msg.GetDiagnostics(); len(diags) > 0 {
		writeRenderErrorLegacyShape(w, diags)
		return
	}
	visualWriteJSON(w, http.StatusOK, resp.Msg)
}

// Validate handles POST /api/orgs/{org}/visual/validate.
func (h *VisualHandler) Validate(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ValidateVisualRequest{OrgId: chi.URLParam(r, "org")}
	if !visualDecodeBody(w, r, 0, req) {
		return
	}
	resp, err := h.service.Validate(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	// Legacy shape: an L1 (render-failure) diagnostic list is a 422 with a
	// bare {"diagnostics":[...]} (no "error" key, unlike Render's 422 shape)
	// of visual.RenderDiagnostic-shaped objects (layer/code/node_id/
	// node_id2/message); an L2 (Stage1+2) diagnostic list — even non-empty —
	// is always 200, of VisualNodeDiagnostic-shaped objects
	// (layer/node_id/line/col/message). RenderDiagnostics/Diagnostics are
	// mutually exclusive on the response (see VisualService.Validate).
	if diags := resp.Msg.GetRenderDiagnostics(); len(diags) > 0 {
		writeValidateL1LegacyShape(w, diags)
		return
	}
	// render_diagnostics is always empty here but still present on the proto
	// message, so drop it — the legacy 200 body is exactly {"diagnostics":[...]}.
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, "render_diagnostics")
}

// UpgradeCheck handles POST /api/orgs/{org}/visual/upgrade-check.
// Computes a structural diff of the graph against the current schema version.
func (h *VisualHandler) UpgradeCheck(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.UpgradeCheckRequest{OrgId: chi.URLParam(r, "org")}
	if !visualDecodeBody(w, r, upgradeCheckMaxBodyBytes, req) {
		return
	}
	resp, err := h.service.UpgradeCheck(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	visualWriteJSON(w, http.StatusOK, resp.Msg)
}

// GraphView handles GET /api/orgs/{org}/pipelines/{id}/graph.
// [reader] — accessible to org readers (graph view is read-only); RBAC is
// enforced by RequireOrgAccess(..., "reader") on this route (router.go).
func (h *VisualHandler) GraphView(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.GraphViewRequest{
		OrgId: chi.URLParam(r, "org"),
		Id:    chi.URLParam(r, "id"),
	}
	resp, err := h.service.GraphView(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	visualWriteJSON(w, http.StatusOK, resp.Msg)
}

// visualDecodeBody reads r.Body (capped at maxBytes when > 0) and, if
// non-empty, protojson-decodes it into msg. Returns false (having already
// written a 400 response) on any read/decode error.
func visualDecodeBody(w http.ResponseWriter, r *http.Request, maxBytes int64, msg proto.Message) bool {
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	// No empty-body shortcut: legacy decoded every one of these bodies with
	// json.Decode, which errors (EOF) on a zero-length body — protojson.Unmarshal
	// does the same, so an empty POST body still 400s here as it did before.
	if err := protojson.Unmarshal(body, msg); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// visualWriteJSON renders msg as legacy-compatible JSON (shim.go's
// MarshalOpts) with the given HTTP status.
func visualWriteJSON(w http.ResponseWriter, status int, msg proto.Message) {
	b, err := MarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:errcheck // response headers already sent
}

// visualRenderDiagnosticJSON mirrors internal/visual.RenderDiagnostic's JSON
// shape exactly, so the legacy render_error envelope (visualDiagnostics ride
// alongside an "error" key, unlike Validate's bare {"diagnostics":[...]})
// stays byte-compatible.
type visualRenderDiagnosticJSON struct {
	Layer   string `json:"layer"`
	Code    string `json:"code"`
	NodeID  string `json:"node_id,omitempty"`
	NodeID2 string `json:"node_id2,omitempty"`
	Message string `json:"message"`
}

func writeRenderErrorLegacyShape(w http.ResponseWriter, diags []*mgmtv1.VisualDiagnostic) {
	respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":       map[string]string{"code": "render_error"},
		"diagnostics": renderDiagnosticJSONs(diags),
	})
}

// writeValidateL1LegacyShape mirrors VisualHandler.Validate's L1
// (render-failure) 422 body exactly: a bare {"diagnostics":[...]} — unlike
// Render's writeRenderErrorLegacyShape, there is no "error" key — of
// visualRenderDiagnosticJSON-shaped objects (layer, code with no omitempty,
// node_id/node_id2 omitempty, message).
func writeValidateL1LegacyShape(w http.ResponseWriter, diags []*mgmtv1.VisualDiagnostic) {
	respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"diagnostics": renderDiagnosticJSONs(diags),
	})
}

// renderDiagnosticJSONs converts VisualDiagnostic protos to
// visualRenderDiagnosticJSON values, the byte-compatible shape for both
// Render's and Validate's L1 diagnostic lists.
func renderDiagnosticJSONs(diags []*mgmtv1.VisualDiagnostic) []visualRenderDiagnosticJSON {
	out := make([]visualRenderDiagnosticJSON, 0, len(diags))
	for _, d := range diags {
		out = append(out, visualRenderDiagnosticJSON{
			Layer: d.GetLayer(), Code: d.GetCode(), NodeID: d.GetNodeId(), NodeID2: d.GetNodeId2(), Message: d.GetMessage(),
		})
	}
	return out
}
