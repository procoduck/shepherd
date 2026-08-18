package mgmtapi

import (
	"log/slog"
	"net/http"

	"shepherd/internal/simulate"
)

// simulateMaxBodyBytes caps simulate request bodies at 256 KiB.
const simulateMaxBodyBytes = 256 * 1024

// SimulateHandler handles S2 simulation REST endpoints.
type SimulateHandler struct{ logger *slog.Logger }

// NewSimulateHandler creates a new SimulateHandler.
func NewSimulateHandler(logger *slog.Logger) *SimulateHandler {
	return &SimulateHandler{logger: logger}
}

// SimulateRelabel handles POST /api/orgs/{org}/simulate/relabel.
func (h *SimulateHandler) SimulateRelabel(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, simulateMaxBodyBytes)
	var req simulate.RelabelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.SampleTargets) == 0 {
		req.SampleTargets = simulate.BuiltinRelabelTargets()
	}
	response, err := simulate.RelabelTrace(req)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, response)
}

// SimulateLogs handles POST /api/orgs/{org}/simulate/logs.
func (h *SimulateHandler) SimulateLogs(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, simulateMaxBodyBytes)
	var req simulate.LogsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.SampleLines) == 0 {
		req.SampleLines = simulate.BuiltinLogLines()
	}
	response, err := simulate.LogTrace(req)
	if err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	respondJSON(w, http.StatusOK, response)
}
