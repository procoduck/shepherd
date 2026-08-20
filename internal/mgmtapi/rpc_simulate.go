package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/config"
	"shepherd/internal/simulate"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// SimulateService implements mgmtv1connect.SimulateServiceHandler — the
// business logic for /api/orgs/{org}/simulate/*, moved here from
// SimulateHandler (simulate.go, now a thin REST shim over this service).
type SimulateService struct {
	store  *store.Store
	cfg    config.SimulatorConfig
	logger *slog.Logger
}

// NewSimulateService constructs a SimulateService. store/cfg back CreateRun
// and GetRun (S3 sandbox runs, VB-1 §6.4); SimulateRelabel/SimulateLogs
// (S2) use neither.
func NewSimulateService(st *store.Store, cfg config.SimulatorConfig, logger *slog.Logger) *SimulateService {
	return &SimulateService{store: st, cfg: cfg, logger: logger}
}

var _ mgmtv1connect.SimulateServiceHandler = (*SimulateService)(nil)

// Run API errors (S3 sandbox runs, VB-1 §6.4/§13 M7).
var (
	// Reaches the user verbatim: the builder renders a failed CreateRun's message
	// straight into the Sandbox run panel, where the machine token this used to
	// carry ("simulator_not_configured") read as a leaked internal code rather
	// than an explanation. Human sentence, matching errRunNotFound below.
	errSimulatorNotConfigured = errors.New("sandbox simulation is not enabled on this server")
	errRunGraphInvalid        = errors.New("graph.kind must be \"alloy-graph/v1\"")
	errRunGraphEmpty          = errors.New("graph must have at least one node")
	errRunIDInvalid           = errors.New("invalid run id")
	errRunNotFound            = errors.New("run not found")
	errTooManyRuns            = errors.New("this org has too many queued or running simulate runs")
)

const (
	defaultRunDuration    = int32(30)
	maxRunDuration        = int32(120) // matches simulate_runs' CHECK (requested_duration_seconds BETWEEN 1 AND 120)
	defaultMaxNonTerminal = 3
)

// CreateRun submits a graph for an S3 sandbox run (VB-1 §6.4 step 1 onward).
// It performs only a cheap structural pre-check synchronously; render,
// transform, gate validation and the sandbox call itself all happen later in
// internal/simulate.RunWorker, with the run recorded as failed rather than
// synchronously rejected — see the run-API spec's decision 13.
func (s *SimulateService) CreateRun(ctx context.Context, req *connect.Request[mgmtv1.CreateRunRequest]) (*connect.Response[mgmtv1.CreateRunResponse], error) {
	if !s.cfg.Enabled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errSimulatorNotConfigured)
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	graphMsg := req.Msg.GetGraph()
	if graphMsg == nil || graphMsg.GetKind() != "alloy-graph/v1" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errRunGraphInvalid)
	}
	if len(graphMsg.GetNodes()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errRunGraphEmpty)
	}

	maxNonTerminal := s.cfg.MaxNonTerminalPerOrg
	if maxNonTerminal <= 0 {
		maxNonTerminal = defaultMaxNonTerminal
	}
	nonTerminal, err := s.store.Queries.CountNonTerminalSimulateRunsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("count non-terminal simulate runs", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("checking run queue"))
	}
	if int(nonTerminal) >= maxNonTerminal {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errTooManyRuns)
	}

	duration := s.clampDuration(req.Msg.GetDurationSeconds())

	doc := graphDocFromProto(graphMsg)
	graphJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("encoding graph"))
	}

	actor := actorFromCtx(ctx)
	run, err := s.store.Queries.CreateSimulateRun(ctx, sqlc.CreateSimulateRunParams{
		OrgID: orgID, Graph: graphJSON, RequestedDurationSeconds: duration, CreatedBy: actor,
	})
	if err != nil {
		s.logger.Error("create simulate run", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not create run"))
	}

	auditLogDetail(ctx, s.store, actor, "user", orgID, "simulate.run.create", "simulate_run", run.ID.String(), map[string]any{
		"node_count":       len(graphMsg.GetNodes()),
		"edge_count":       len(graphMsg.GetEdges()),
		"duration_seconds": duration,
	})

	return connect.NewResponse(&mgmtv1.CreateRunResponse{RunId: run.ID.String()}), nil
}

// clampDuration applies the server default (0 -> cfg.Duration, itself
// defaulted to 30s) and ceiling (cfg.MaxDuration, itself defaulted to 120s
// and never above the migration's own 120s CHECK).
func (s *SimulateService) clampDuration(requested int32) int32 {
	def := defaultRunDuration
	if s.cfg.Duration > 0 {
		def = int32(s.cfg.Duration / time.Second) //nolint:gosec // config-bounded duration
	}
	ceiling := int64(maxRunDuration)
	if cfgCeiling := int64(s.cfg.MaxDuration / time.Second); s.cfg.MaxDuration > 0 && cfgCeiling < ceiling {
		ceiling = cfgCeiling
	}
	switch {
	case requested <= 0:
		return def
	case int64(requested) > ceiling:
		return int32(ceiling) // capped at maxRunDuration (120) above; always in int32 range
	default:
		return requested
	}
}

// GetRun returns a run's current state, including results once terminal.
func (s *SimulateService) GetRun(ctx context.Context, req *connect.Request[mgmtv1.GetRunRequest]) (*connect.Response[mgmtv1.SimulateRun], error) {
	run, err := s.loadRun(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	var queuePosition int32
	if run.Status == simulate.RunStatusQueued {
		// Best-effort — not transactionally consistent with the claim loop;
		// cosmetic UI progress text only (run-API spec Risks).
		n, cntErr := s.store.Queries.CountQueuedSimulateRunsBefore(ctx, run.ID)
		if cntErr == nil {
			queuePosition = int32(n) + 1 //nolint:gosec // bounded by MaxNonTerminalPerOrg * org count, nowhere near int32 overflow
		}
	}
	proto, err := simulateRunToProto(run, queuePosition)
	if err != nil {
		s.logger.Error("decode simulate run", "run_id", run.ID.String(), "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("decoding run"))
	}
	return connect.NewResponse(proto), nil
}

// loadRun mirrors PipelineService.loadPipeline (rpc_pipeline.go) byte-for-
// byte in structure: a global lookup by id, then an org_id equality check
// in Go returning NotFound (never PermissionDenied) on mismatch, so an
// org-B admin cannot use the response code to confirm a run id belongs to
// org A. GetRun is the only caller — CreateRun never needs it, since the
// row it inserts is born with the caller's own org_id.
func (s *SimulateService) loadRun(ctx context.Context, orgIDStr, idStr string) (sqlc.SimulateRun, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.SimulateRun{}, connect.NewError(connect.CodeInvalidArgument, errRunIDInvalid)
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.SimulateRun{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	run, err := s.store.Queries.GetSimulateRunByID(ctx, id)
	if err != nil {
		return sqlc.SimulateRun{}, connect.NewError(connect.CodeNotFound, errRunNotFound)
	}
	if run.OrgID != orgID {
		return sqlc.SimulateRun{}, connect.NewError(connect.CodeNotFound, errRunNotFound)
	}
	return run, nil
}

// SimulateRelabel traces sample targets through a relabel rule chain.
func (s *SimulateService) SimulateRelabel(_ context.Context, req *connect.Request[mgmtv1.SimulateRelabelRequest]) (*connect.Response[mgmtv1.SimulateRelabelResponse], error) {
	simReq := simulate.RelabelRequest{
		Rules:         relabelRulesFromProto(req.Msg.GetRules()),
		SampleTargets: structsToLabelMaps(req.Msg.GetSampleTargets()),
	}
	if len(simReq.SampleTargets) == 0 {
		simReq.SampleTargets = simulate.BuiltinRelabelTargets()
	}
	result, err := simulate.RelabelTrace(simReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(relabelResponseToProto(result)), nil
}

// SimulateLogs traces sample lines through a logs processing stage chain.
func (s *SimulateService) SimulateLogs(_ context.Context, req *connect.Request[mgmtv1.SimulateLogsRequest]) (*connect.Response[mgmtv1.SimulateLogsResponse], error) {
	simReq := simulate.LogsRequest{
		Stages:      stageSpecsFromProto(req.Msg.GetStages()),
		SampleLines: req.Msg.GetSampleLines(),
	}
	if len(simReq.SampleLines) == 0 {
		simReq.SampleLines = simulate.BuiltinLogLines()
	}
	result, err := simulate.LogTrace(simReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(logsResponseToProto(result)), nil
}

// -- proto conversions --

func relabelRulesFromProto(rules []*mgmtv1.RelabelRule) []simulate.RelabelRule {
	out := make([]simulate.RelabelRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, simulate.RelabelRule{
			SourceLabels: r.GetSourceLabels(),
			Separator:    r.GetSeparator(),
			Regex:        r.GetRegex(),
			TargetLabel:  r.GetTargetLabel(),
			Replacement:  r.GetReplacement(),
			Action:       r.GetAction(),
			Modulus:      r.GetModulus(),
		})
	}
	return out
}

// structsToLabelMaps converts each Struct's fields to a map[string]string
// label set. sample_targets is a repeated Struct (proto has no repeated-map
// type — see simulate.proto); values are read as strings, falling back to
// fmt.Sprint for the rare non-string JSON value.
func structsToLabelMaps(structs []*structpb.Struct) []map[string]string {
	out := make([]map[string]string, 0, len(structs))
	for _, st := range structs {
		m := map[string]string{}
		if st != nil {
			for k, v := range st.AsMap() {
				if sv, ok := v.(string); ok {
					m[k] = sv
				} else {
					m[k] = fmt.Sprint(v)
				}
			}
		}
		out = append(out, m)
	}
	return out
}

func relabelResponseToProto(r simulate.RelabelResponse) *mgmtv1.SimulateRelabelResponse {
	traces := make([]*mgmtv1.TargetTrace, 0, len(r.Traces))
	for _, t := range r.Traces {
		steps := make([]*mgmtv1.RelabelStep, 0, len(t.Steps))
		for _, st := range t.Steps {
			steps = append(steps, &mgmtv1.RelabelStep{
				RuleIndex: int32(st.RuleIndex), //nolint:gosec // G115: bounded by maxRelabelRules (128)
				Action:    st.Action, Before: st.Before, After: st.After, Kept: st.Kept,
			})
		}
		traces = append(traces, &mgmtv1.TargetTrace{Input: t.Input, Steps: steps, Output: t.Output, Kept: t.Kept})
	}
	return &mgmtv1.SimulateRelabelResponse{Traces: traces}
}

func stageSpecsFromProto(specs []*mgmtv1.StageSpec) []simulate.StageSpec {
	out := make([]simulate.StageSpec, 0, len(specs))
	for _, sp := range specs {
		out = append(out, simulate.StageSpec{
			Type: sp.GetType(), Expressions: sp.GetExpressions(), Source: sp.GetSource(),
			Separator: sp.GetSeparator(), Expression: sp.GetExpression(), Labels: sp.GetLabels(),
			DropLabels: sp.GetDropLabels(), DropValue: sp.GetDropValue(), Template: sp.GetTemplate(), FirstLine: sp.GetFirstline(),
		})
	}
	return out
}

func logsResponseToProto(r simulate.LogsResponse) *mgmtv1.SimulateLogsResponse {
	traces := make([]*mgmtv1.LineTrace, 0, len(r.Traces))
	for _, t := range r.Traces {
		steps := make([]*mgmtv1.StageEffect, 0, len(t.Steps))
		for _, st := range t.Steps {
			steps = append(steps, &mgmtv1.StageEffect{
				StageIndex: int32(st.StageIndex), //nolint:gosec // G115: bounded by maxLogStages (64)
				StageType:  st.StageType, Simulated: st.Simulated,
				LineBefore: st.LineBefore, LineAfter: st.LineAfter, LabelsBefore: st.LabelsBefore, LabelsAfter: st.LabelsAfter,
				Dropped: st.Dropped, Note: st.Note,
			})
		}
		traces = append(traces, &mgmtv1.LineTrace{Input: t.Input, Steps: steps, Output: t.Output, Dropped: t.Dropped})
	}
	return &mgmtv1.SimulateLogsResponse{Traces: traces}
}

// -- Run (S3 sandbox run) proto conversion --

// simulateRunToProto decodes a stored run's JSONB columns and assembles the
// full SimulateRun document GetRun returns. queuePosition is supplied by the
// caller (computed at read time, per decision 8 — never stored) and used
// only while status is still "queued".
func simulateRunToProto(run sqlc.SimulateRun, queuePosition int32) (*mgmtv1.SimulateRun, error) {
	var rewrites []simulate.Rewrite
	if err := json.Unmarshal(run.Rewrites, &rewrites); err != nil {
		return nil, fmt.Errorf("decoding rewrites: %w", err)
	}
	var series []simulate.RunSeries
	if err := json.Unmarshal(run.CapturedSeries, &series); err != nil {
		return nil, fmt.Errorf("decoding captured_series: %w", err)
	}
	var logLines []simulate.RunLogLine
	if err := json.Unmarshal(run.CapturedLogLines, &logLines); err != nil {
		return nil, fmt.Errorf("decoding captured_log_lines: %w", err)
	}
	var health []simulate.RunComponentHealth
	if err := json.Unmarshal(run.ComponentHealth, &health); err != nil {
		return nil, fmt.Errorf("decoding component_health: %w", err)
	}
	var gate []simulate.RunGateDiagnostic
	if err := json.Unmarshal(run.GateDiagnostics, &gate); err != nil {
		return nil, fmt.Errorf("decoding gate_diagnostics: %w", err)
	}

	pos := int32(0)
	if run.Status == simulate.RunStatusQueued {
		pos = queuePosition
	}

	return &mgmtv1.SimulateRun{
		Id:                       run.ID.String(),
		OrgId:                    run.OrgID.String(),
		Status:                   run.Status,
		CreatedAt:                timestampFromPg(run.CreatedAt),
		StartedAt:                timestampFromPg(run.StartedAt),
		FinishedAt:               timestampFromPg(run.FinishedAt),
		RequestedDurationSeconds: run.RequestedDurationSeconds,
		QueuePosition:            pos,
		Rewrites:                 runRewritesToProto(rewrites),
		CapturedSeries:           runSeriesToProto(series),
		CapturedLogLines:         runLogLinesToProto(logLines),
		ComponentHealth:          runHealthToProto(health),
		GateDiagnostics:          runGateDiagnosticsToProto(gate),
		StderrTail:               run.StderrTail,
		ErrorCode:                run.ErrorCode,
		ErrorMessage:             run.ErrorMessage,
		FidelityNote:             simulate.FidelityNoteS3,
	}, nil
}

func runRewritesToProto(in []simulate.Rewrite) []*mgmtv1.SimulateRewrite {
	out := make([]*mgmtv1.SimulateRewrite, 0, len(in))
	for i := range in {
		r := &in[i]
		out = append(out, &mgmtv1.SimulateRewrite{
			NodeId: r.NodeID, NodeLabel: r.NodeLabel, Component: r.Component,
			Kind: string(r.Kind), Detail: r.Detail,
		})
	}
	return out
}

func runSeriesToProto(in []simulate.RunSeries) []*mgmtv1.CapturedSeries {
	out := make([]*mgmtv1.CapturedSeries, 0, len(in))
	for _, s := range in {
		out = append(out, &mgmtv1.CapturedSeries{
			Name: s.Name, Labels: s.Labels,
			SampleCount: int32(s.SampleCount), //nolint:gosec // capped at maxCapturedSeries samples
		})
	}
	return out
}

func runLogLinesToProto(in []simulate.RunLogLine) []*mgmtv1.CapturedLogLine {
	out := make([]*mgmtv1.CapturedLogLine, 0, len(in))
	for _, l := range in {
		out = append(out, &mgmtv1.CapturedLogLine{Labels: l.Labels, Line: l.Line})
	}
	return out
}

func runHealthToProto(in []simulate.RunComponentHealth) []*mgmtv1.ComponentHealth {
	out := make([]*mgmtv1.ComponentHealth, 0, len(in))
	for _, h := range in {
		out = append(out, &mgmtv1.ComponentHealth{
			NodeId: h.NodeID, NodeLabel: h.NodeLabel, Component: h.Component,
			HealthState: h.HealthState, Message: h.Message,
		})
	}
	return out
}

func runGateDiagnosticsToProto(in []simulate.RunGateDiagnostic) []*mgmtv1.VisualNodeDiagnostic {
	out := make([]*mgmtv1.VisualNodeDiagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, &mgmtv1.VisualNodeDiagnostic{
			Layer: d.Layer, NodeId: d.NodeID,
			Line: int32(d.Line), Col: int32(d.Col), //nolint:gosec // line/col from a rendered Alloy file, well within int32
			Message: d.Message,
		})
	}
	return out
}
