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
	"shepherd/internal/config"
	"shepherd/internal/store"
)

// simulateMaxBodyBytes caps simulate request bodies at 256 KiB.
const simulateMaxBodyBytes = 256 * 1024

// SimulateHandler handles S2 simulation REST endpoints — a thin shim over
// SimulateService (rpc_simulate.go). It parses URL params/body into the
// proto request, calls the service directly in-process, and renders the
// response as legacy-compatible JSON. No business logic lives here.
type SimulateHandler struct {
	service *SimulateService
	logger  *slog.Logger
}

// NewSimulateHandler creates a new SimulateHandler.
func NewSimulateHandler(st *store.Store, cfg config.SimulatorConfig, logger *slog.Logger) *SimulateHandler {
	return &SimulateHandler{service: NewSimulateService(st, cfg, logger), logger: logger}
}

// SimulateRelabel handles POST /api/orgs/{org}/simulate/relabel.
func (h *SimulateHandler) SimulateRelabel(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.SimulateRelabelRequest{OrgId: chi.URLParam(r, "org")}
	if !simulateDecodeBody(w, r, req) {
		return
	}
	resp, err := h.service.SimulateRelabel(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	simulateWriteJSON(w, resp.Msg)
}

// SimulateLogs handles POST /api/orgs/{org}/simulate/logs.
func (h *SimulateHandler) SimulateLogs(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.SimulateLogsRequest{OrgId: chi.URLParam(r, "org")}
	if !simulateDecodeBody(w, r, req) {
		return
	}
	resp, err := h.service.SimulateLogs(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	simulateWriteJSON(w, resp.Msg)
}

// CreateRun handles POST /api/orgs/{org}/simulate/runs.
func (h *SimulateHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.CreateRunRequest{OrgId: chi.URLParam(r, "org")}
	if !simulateDecodeBody(w, r, req) {
		return
	}
	resp, err := h.service.CreateRun(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	simulateWriteJSON(w, resp.Msg)
}

// GetRun handles GET /api/orgs/{org}/simulate/runs/{id}.
func (h *SimulateHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	req := &mgmtv1.GetRunRequest{OrgId: chi.URLParam(r, "org"), Id: chi.URLParam(r, "id")}
	resp, err := h.service.GetRun(r.Context(), connect.NewRequest(req))
	if err != nil {
		WriteConnectError(w, err)
		return
	}
	simulateWriteJSON(w, resp.Msg)
}

// simulateDecodeBody reads r.Body (capped at simulateMaxBodyBytes) and, if
// non-empty, protojson-decodes it into msg. Returns false (having already
// written a 400 response) on any read/decode error.
func simulateDecodeBody(w http.ResponseWriter, r *http.Request, msg proto.Message) bool {
	r.Body = http.MaxBytesReader(w, r.Body, simulateMaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return false
	}
	// No empty-body shortcut: legacy decoded these bodies with json.Decode,
	// which errors (EOF) on a zero-length body — protojson.Unmarshal does the
	// same, so an empty POST body still 400s here as it did before.
	if err := protojson.Unmarshal(body, msg); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// simulateWriteJSON renders msg as legacy-compatible JSON (shim.go's
// MarshalOpts). Every simulate REST handler answers 200 on success — errors
// go through WriteConnectError instead — so there is no status parameter.
func simulateWriteJSON(w http.ResponseWriter, msg proto.Message) {
	b, err := MarshalOpts.Marshal(msg)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b) //nolint:errcheck // response headers already sent
}
