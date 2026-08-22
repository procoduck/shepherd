package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/auth"
	"shepherd/internal/validate"
)

// PipelinesHandler is a thin REST shim over PipelineService for
// /api/orgs/{org}/pipelines*: it parses URL params/query/body into the
// shepherd.mgmt.v1 proto request, calls the service method directly
// (in-process, not over HTTP), and renders the response with the shim
// helpers in shim.go so the legacy JSON stays byte-compatible. No business
// logic lives here — see rpc_pipeline.go.
type PipelinesHandler struct {
	svc *PipelineService
}

// NewPipelinesHandler creates a pipelines REST shim backed by svc.
func NewPipelinesHandler(svc *PipelineService) *PipelinesHandler {
	return &PipelinesHandler{svc: svc}
}

// pipelineRequest is the legacy wire shape for create/update/validate pipeline bodies.
type pipelineRequest struct {
	Name        string          `json:"name"`
	Contents    string          `json:"contents"`
	Matchers    []string        `json:"matchers"`
	Source      string          `json:"source,omitempty"`
	WizardState json.RawMessage `json:"wizard_state,omitempty"`
}

// wizardStateStruct converts a raw JSON wizard_state payload (if any) to a
// *structpb.Struct for the proto request. An absent/empty payload, or one
// that isn't a JSON object, yields a nil Struct — matching the legacy
// handler's zero-value (json.RawMessage(nil)) behavior for "not provided".
func wizardStateStruct(raw json.RawMessage) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	var s structpb.Struct
	if err := protojson.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// writePipelineSaveError renders a CreatePipeline/UpdatePipeline error. A
// failed Stage1/2 validation gate carries its diagnostics via
// pipelineValidationError (rpc_pipeline.go); the shim reproduces the legacy
// {"error":{"code":"validation_failed",...},"diagnostics":[...]} envelope
// for that one case exactly. Every other error path uses the generic
// connect error shim (shim.go's WriteConnectError).
func writePipelineSaveError(w http.ResponseWriter, err error) {
	var vfe *pipelineValidationError
	if errors.As(err, &vfe) {
		diags := make([]validate.Diagnostic, len(vfe.Diagnostics))
		copy(diags, vfe.Diagnostics)
		respondJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":       map[string]any{"code": "validation_failed", "message": "pipeline failed validation"},
			"diagnostics": diags,
		})
		return
	}
	WriteConnectError(w, err)
}

// pipelineOmitFields names the Pipeline fields the legacy pipelineResponse
// struct marked `,omitempty`: revision is never populated by any handler
// (always 0/omitted); revisions is populated only by GetPipeline, and only
// when the pipeline actually has revision history.
var pipelineOmitFields = []string{"revision", "revisions"} //nolint:gochecknoglobals // shared, read-only field list

// List GET /api/orgs/{org}/pipelines
func (h *PipelinesHandler) List(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListPipelinesRequest{
		OrgId:        chi.URLParam(r, "org"),
		NeedsUpgrade: r.URL.Query().Get("needs_upgrade") == "true",
	}
	resp, err := h.svc.ListPipelines(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, pipelineOmitFields...)
}

// Create POST /api/orgs/{org}/pipelines
func (h *PipelinesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body pipelineRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.CreatePipelineRequest{
		OrgId:       chi.URLParam(r, "org"),
		Name:        body.Name,
		Contents:    body.Contents,
		Matchers:    body.Matchers,
		Source:      body.Source,
		WizardState: wizardStateStruct(body.WizardState),
	}
	resp, err := h.svc.CreatePipeline(r.Context(), connect.NewRequest(req))
	if err != nil {
		writePipelineSaveError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusCreated, resp.Msg, pipelineOmitFields...)
}

// Get GET /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Get(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.GetPipelineRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	resp, err := h.svc.GetPipeline(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, pipelineOmitFields...)
}

// Update PUT /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Update(w http.ResponseWriter, r *http.Request) {
	var body pipelineRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.UpdatePipelineRequest{
		OrgId:       chi.URLParam(r, "org"),
		Id:          chi.URLParam(r, "id"),
		Name:        body.Name,
		Contents:    body.Contents,
		Matchers:    body.Matchers,
		Source:      body.Source,
		WizardState: wizardStateStruct(body.WizardState),
	}
	resp, err := h.svc.UpdatePipeline(r.Context(), connect.NewRequest(req))
	if err != nil {
		writePipelineSaveError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, pipelineOmitFields...)
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
	orgID, id := chi.URLParam(r, "org"), chi.URLParam(r, "id")
	var (
		resp *connect.Response[mgmtv1.Pipeline]
		err  error
	)
	if enable {
		resp, err = h.svc.EnablePipeline(r.Context(), connect.NewRequest(&mgmtv1.EnablePipelineRequest{OrgId: orgID, Id: id}))
	} else {
		resp, err = h.svc.DisablePipeline(r.Context(), connect.NewRequest(&mgmtv1.DisablePipelineRequest{OrgId: orgID, Id: id}))
	}
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSONOmit(w, http.StatusOK, resp.Msg, pipelineOmitFields...)
}

// Delete DELETE /api/orgs/{org}/pipelines/{id}
func (h *PipelinesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.DeletePipelineRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	if _, err := h.svc.DeletePipeline(r.Context(), connect.NewRequest(req)); err != nil {
		WriteConnectError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListRevisions GET /api/orgs/{org}/pipelines/{id}/revisions
func (h *PipelinesHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListRevisionsRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	resp, err := h.svc.ListRevisions(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// Validate POST /api/orgs/{org}/pipelines/validate
func (h *PipelinesHandler) Validate(w http.ResponseWriter, r *http.Request) {
	var body pipelineRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	req := &mgmtv1.ValidatePipelineRequest{OrgId: chi.URLParam(r, "org"), Name: body.Name, Contents: body.Contents}
	resp, err := h.svc.ValidatePipeline(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	status := http.StatusOK
	if !resp.Msg.GetValid() {
		status = http.StatusUnprocessableEntity
	}
	writeProtoJSON(w, status, resp.Msg)
}

// PreviewMatches GET /api/orgs/{org}/pipelines/{id}/preview-matches
func (h *PipelinesHandler) PreviewMatches(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.PreviewMatchesRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	resp, err := h.svc.PreviewMatches(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	writeProtoJSON(w, http.StatusOK, resp.Msg)
}

// actorFromCtx returns the authenticated actor identity from the request
// context: a machine caller's service account name (prefixed "svcacct:",
// matching auditLogDetail's identical prefix — see helpers.go) when the
// request is machine-authenticated, otherwise the human session's actor.
// Returns "anonymous" if neither is set (should not happen in practice).
func actorFromCtx(ctx context.Context) string {
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		return "svcacct:" + sa.Name
	}
	return auth.ActorFromCtx(ctx)
}
