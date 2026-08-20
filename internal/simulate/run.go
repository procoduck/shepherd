package simulate

// This file defines the shapes shared between internal/simulate.RunWorker
// (which writes them into simulate_runs' JSONB columns) and
// internal/mgmtapi's SimulateService (which reads them back into
// mgmtv1.SimulateRun) — see the run-API spec's FILES section for
// rpc_simulate.go. Kept here, not in mgmtapi, so the worker has no reason to
// import mgmtapi (which already imports internal/simulate).

// Run status values (SimulateRun.status). Closed set, plain strings per the
// repo's documented "enum-like fields stay strings" contract rule.
const (
	RunStatusQueued    = "queued"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusExpired   = "expired"
)

// Run error codes (SimulateRun.error_code, set iff status == failed).
const (
	RunErrorCannotStub           = "cannot_stub"
	RunErrorGateFailed           = "gate_failed"
	RunErrorSimulatorUnavailable = "simulator_unavailable"
	RunErrorTimeout              = "timeout"
	RunErrorInternal             = "internal"
)

// FidelityNoteS3 is the fixed one-line fidelity note every S3 sandbox run
// carries (VB-1 design doc §6.5: "Each results view carries a one-line
// fidelity note naming its tier's limits").
const FidelityNoteS3 = "Sandbox run (tier S3): destinations point at this harness rather than your real backends, and discovery/log sources are replaced by synthetic data. Captured series/logs show whether Alloy would deliver correctly, not whether your production endpoints or credentials do — component health is not evidence of delivery."

// RunRewrite is the JSONB-stored shape of one disclosure entry
// (simulate_runs.rewrites). It is Rewrite itself: the JSON tags already
// match SimulateRewrite's proto fields 1:1, so no separate storage type is
// needed for this column.

// RunSeries is the JSONB-stored shape of one simulate_runs.captured_series
// entry — the subset of ClientSeries the SimulateRun proto exposes
// (CapturedSeries: name, labels, sample_count).
type RunSeries struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	SampleCount int               `json:"sample_count"`
}

// RunLogLine is the JSONB-stored shape of one simulate_runs.captured_log_lines
// entry (CapturedLogLine: labels, line).
type RunLogLine struct {
	Labels map[string]string `json:"labels"`
	Line   string            `json:"line"`
}

// RunComponentHealth is the JSONB-stored shape of one
// simulate_runs.component_health entry (ComponentHealth proto message).
// NodeLabel/Component are resolved from the AUTHORED graph (not the
// transformed one) at write time, because the simulator's response only
// echoes node_id via the component_index — see internal/simulate.RunWorker.
type RunComponentHealth struct {
	NodeID      string `json:"node_id"`
	NodeLabel   string `json:"node_label"`
	Component   string `json:"component"`
	HealthState string `json:"health_state"`
	Message     string `json:"message"`
}

// RunGateDiagnostic is the JSONB-stored shape of one
// simulate_runs.gate_diagnostics entry (mgmtv1.VisualNodeDiagnostic).
type RunGateDiagnostic struct {
	Layer   string `json:"layer"`
	NodeID  string `json:"node_id"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
}
