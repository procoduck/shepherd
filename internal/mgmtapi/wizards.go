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
	"shepherd/internal/store"
	"shepherd/internal/validate"
)

// WizardHandler handles /api/orgs/{org}/wizards routes — a thin shim over
// WizardService (rpc_wizard.go). It parses URL params/body into the proto
// request, calls the service directly in-process, and renders the response
// as legacy-compatible JSON. No business logic lives here.
type WizardHandler struct {
	service *WizardService
	logger  *slog.Logger
}

// NewWizardHandler creates a wizard handler.
func NewWizardHandler(st *store.Store, v *validate.Validator, logger *slog.Logger) *WizardHandler {
	return &WizardHandler{service: NewWizardService(st, v, logger), logger: logger}
}

// ListWizards returns all registered wizard kinds.
func (h *WizardHandler) ListWizards(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.ListWizardsRequest{OrgId: chi.URLParam(r, "org")}
	resp, err := h.service.ListWizards(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	wizardWriteJSON(w, http.StatusOK, resp.Msg)
}

// GetWizardSchema returns the schema for a specific wizard kind.
func (h *WizardHandler) GetWizardSchema(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.GetWizardSchemaRequest{OrgId: chi.URLParam(r, "org"), Kind: chi.URLParam(r, "kind")}
	resp, err := h.service.GetWizardSchema(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	wizardWriteJSON(w, http.StatusOK, resp.Msg)
}

// RenderWizard renders wizard state into contents + diagnostics + match
// preview without persisting anything (spec §12).
func (h *WizardHandler) RenderWizard(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req := &mgmtv1.RenderWizardRequest{OrgId: chi.URLParam(r, "org")}
	if err := protojson.Unmarshal(body, req); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	resp, err := h.service.RenderWizard(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	wizardWriteJSON(w, http.StatusOK, resp.Msg)
}

// CommitWizard generates a pipeline from wizard state and creates it.
func (h *WizardHandler) CommitWizard(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req := &mgmtv1.CommitWizardRequest{OrgId: chi.URLParam(r, "org")}
	if err := protojson.Unmarshal(body, req); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	resp, err := h.service.CommitWizard(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	wizardWriteJSON(w, http.StatusCreated, resp.Msg)
}

// wizardWriteJSON renders msg as legacy-compatible JSON (shim.go's
// MarshalOpts) with the given HTTP status.
func wizardWriteJSON(w http.ResponseWriter, status int, msg proto.Message) {
	b, err := MarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b) //nolint:errcheck // response headers already sent
}
