package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// NewServer builds the MCP server: one tool per entry in toolProcedures,
// each a thin argument-conversion wrapper around a single Backend call
// (b's http.Client already carries the propose-scoped credential and the
// fixed on-behalf-of claim — see backend.go). impl/version name the server
// to MCP clients; pass shepherd's build version.
func NewServer(b *Backend, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "shepherd",
		Title:   "Shepherd (read + propose)",
		Version: version,
	}, &mcp.ServerOptions{
		Instructions: "Read Shepherd fleet/pipeline state, validate and simulate pipeline " +
			"content, and prepare pipeline proposals. This server cannot apply any change " +
			"to production: every write-shaped action is refused by the underlying " +
			"propose-capability credential (docs/gateway-tier-plan.md G14). To make a " +
			"proposed change real, a human with Shepherd access must apply it themselves " +
			"through the Shepherd UI or their own authenticated session.",
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_collectors",
		Description: "List collectors (Alloy agents) in an org, with each one's remote-config health and last-seen time.",
	}, b.listCollectors)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_collector",
		Description: "Get one collector's detail, including its per-instance health.",
	}, b.getCollector)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_fleet_attributes",
		Description: "List the distinct attribute values (cluster, role, and any others) collectors in an org report — useful for writing pipeline matchers.",
	}, b.listFleetAttributes)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_pipelines",
		Description: "List pipelines in an org.",
	}, b.listPipelines)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pipeline",
		Description: "Get one pipeline's current content, matchers, and revision history.",
	}, b.getPipeline)

	mcp.AddTool(s, &mcp.Tool{
		Name: "validate_pipeline",
		Description: "Render and validate Alloy pipeline content (syntax + component checks against the pinned schema) " +
			"and report its derived telemetry signals (metrics/logs/traces/profiles). Does not persist anything.",
	}, b.validatePipeline)

	mcp.AddTool(s, &mcp.Tool{
		Name: "preview_matches",
		Description: "Compute the blast radius of an EXISTING pipeline: which collectors its currently-stored matchers select. " +
			"Does not reflect matcher changes you are only proposing — see propose_pipeline_revision.",
	}, b.previewMatches)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_relabel",
		Description: "Trace sample Prometheus targets through a chain of relabel rules, step by step, without touching any real pipeline.",
	}, b.simulateRelabel)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "simulate_logs",
		Description: "Trace sample log lines through a chain of loki.process stages, step by step, without touching any real pipeline.",
	}, b.simulateLogs)

	mcp.AddTool(s, &mcp.Tool{
		Name: "create_sandbox_run",
		Description: "Start a sandboxed (S3) simulation run of a visual-builder graph document against the real Alloy binary, " +
			"capturing series/log lines/component health. Only usable for pipelines built as a graph (source=\"visual\"); " +
			"raw Alloy text has no graph to run. Returns a run id — poll get_sandbox_run for results.",
	}, b.createSandboxRun)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_sandbox_run",
		Description: "Get a sandbox run's status and, once terminal, its captured results.",
	}, b.getSandboxRun)

	mcp.AddTool(s, &mcp.Tool{
		Name: "propose_pipeline_revision",
		Description: "Validate candidate Alloy pipeline content and (for an existing pipeline) compute its blast radius, " +
			"returning a proposal package for a human to review and apply. This tool NEVER writes the change — it cannot: " +
			"this server's credential has propose capability only (docs/gateway-tier-plan.md G14/G15). Hand the returned " +
			"proposal to a human with Shepherd access; applying it is an ordinary CreatePipeline/UpdatePipeline call made " +
			"through their own session.",
	}, b.proposePipelineRevision)

	return s
}

// -- shared argument shapes --

type orgArg struct {
	OrgID string `json:"org_id" jsonschema:"the Shepherd org UUID"`
}

type orgIDArg struct {
	OrgID string `json:"org_id" jsonschema:"the Shepherd org UUID"`
	ID    string `json:"id" jsonschema:"the resource UUID"`
}

// -- list_collectors --

type listCollectorsOut struct {
	Items []CollectorView `json:"items"`
	Total int32           `json:"total"`
}

func (b *Backend) listCollectors(ctx context.Context, _ *mcp.CallToolRequest, in orgArg) (*mcp.CallToolResult, listCollectorsOut, error) {
	resp, err := b.Fleet.ListCollectors(ctx, connect.NewRequest(&mgmtv1.ListCollectorsRequest{OrgId: in.OrgID}))
	if err != nil {
		return nil, listCollectorsOut{}, err
	}
	out := listCollectorsOut{Total: resp.Msg.GetTotal()}
	for _, c := range resp.Msg.GetItems() {
		out.Items = append(out.Items, toCollectorView(c))
	}
	return nil, out, nil
}

// -- get_collector --

func (b *Backend) getCollector(ctx context.Context, _ *mcp.CallToolRequest, in orgIDArg) (*mcp.CallToolResult, CollectorView, error) {
	resp, err := b.Fleet.GetCollector(ctx, connect.NewRequest(&mgmtv1.GetCollectorRequest{OrgId: in.OrgID, Id: in.ID}))
	if err != nil {
		return nil, CollectorView{}, err
	}
	return nil, toCollectorView(resp.Msg), nil
}

// -- list_fleet_attributes --

func (b *Backend) listFleetAttributes(ctx context.Context, _ *mcp.CallToolRequest, in orgArg) (*mcp.CallToolResult, map[string]any, error) {
	resp, err := b.Fleet.ListAttributes(ctx, connect.NewRequest(&mgmtv1.ListAttributesRequest{OrgId: in.OrgID}))
	if err != nil {
		return nil, nil, err
	}
	if a := resp.Msg.GetAttributes(); a != nil {
		return nil, a.AsMap(), nil
	}
	return nil, map[string]any{}, nil
}

// -- list_pipelines --

type listPipelinesIn struct {
	OrgID        string `json:"org_id" jsonschema:"the Shepherd org UUID"`
	NeedsUpgrade bool   `json:"needs_upgrade,omitempty" jsonschema:"restrict to visual pipelines whose schema version is behind current"`
}

type listPipelinesOut struct {
	Items []PipelineView `json:"items"`
	Total int32          `json:"total"`
}

func (b *Backend) listPipelines(ctx context.Context, _ *mcp.CallToolRequest, in listPipelinesIn) (*mcp.CallToolResult, listPipelinesOut, error) {
	resp, err := b.Pipeline.ListPipelines(ctx, connect.NewRequest(&mgmtv1.ListPipelinesRequest{OrgId: in.OrgID, NeedsUpgrade: in.NeedsUpgrade}))
	if err != nil {
		return nil, listPipelinesOut{}, err
	}
	out := listPipelinesOut{Total: resp.Msg.GetTotal()}
	for _, p := range resp.Msg.GetItems() {
		out.Items = append(out.Items, toPipelineView(p))
	}
	return nil, out, nil
}

// -- get_pipeline --

func (b *Backend) getPipeline(ctx context.Context, _ *mcp.CallToolRequest, in orgIDArg) (*mcp.CallToolResult, PipelineView, error) {
	resp, err := b.Pipeline.GetPipeline(ctx, connect.NewRequest(&mgmtv1.GetPipelineRequest{OrgId: in.OrgID, Id: in.ID}))
	if err != nil {
		return nil, PipelineView{}, err
	}
	return nil, toPipelineView(resp.Msg), nil
}

// -- validate_pipeline --

type validatePipelineIn struct {
	OrgID    string `json:"org_id" jsonschema:"the Shepherd org UUID"`
	Name     string `json:"name,omitempty" jsonschema:"a display name for diagnostics; defaults to \"preview\""`
	Contents string `json:"contents" jsonschema:"the candidate Alloy pipeline text"`
}

type validatePipelineOut struct {
	Valid             bool             `json:"valid"`
	Diagnostics       []DiagnosticView `json:"diagnostics,omitempty"`
	Signals           []string         `json:"signals,omitempty"`
	SignalsProven     bool             `json:"signals_proven"`
	UnknownComponents []string         `json:"unknown_components,omitempty"`
}

func (b *Backend) validatePipeline(ctx context.Context, _ *mcp.CallToolRequest, in validatePipelineIn) (*mcp.CallToolResult, validatePipelineOut, error) {
	resp, err := b.Pipeline.ValidatePipeline(ctx, connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
		OrgId: in.OrgID, Name: in.Name, Contents: in.Contents,
	}))
	if err != nil {
		return nil, validatePipelineOut{}, err
	}
	return nil, validatePipelineOut{
		Valid:             resp.Msg.GetValid(),
		Diagnostics:       toDiagnosticViews(resp.Msg.GetDiagnostics()),
		Signals:           resp.Msg.GetSignals(),
		SignalsProven:     resp.Msg.GetSignalsProven(),
		UnknownComponents: resp.Msg.GetUnknownComponents(),
	}, nil
}

// -- preview_matches --

type previewMatchesOut struct {
	Collectors []MatchedCollectorView `json:"collectors"`
}

func (b *Backend) previewMatches(ctx context.Context, _ *mcp.CallToolRequest, in orgIDArg) (*mcp.CallToolResult, previewMatchesOut, error) {
	resp, err := b.Pipeline.PreviewMatches(ctx, connect.NewRequest(&mgmtv1.PreviewMatchesRequest{OrgId: in.OrgID, Id: in.ID}))
	if err != nil {
		return nil, previewMatchesOut{}, err
	}
	return nil, previewMatchesOut{Collectors: toMatchedCollectorViews(resp.Msg.GetCollectors())}, nil
}

// -- simulate_relabel --

type relabelRuleIn struct {
	SourceLabels []string `json:"source_labels,omitempty"`
	Separator    string   `json:"separator,omitempty"`
	Regex        string   `json:"regex,omitempty"`
	TargetLabel  string   `json:"target_label,omitempty"`
	Replacement  string   `json:"replacement,omitempty"`
	Action       string   `json:"action" jsonschema:"replace, keep, drop, labelmap, labeldrop, labelkeep, lowercase, uppercase, keepequal, dropequal, or hashmod"`
	Modulus      uint64   `json:"modulus,omitempty"`
}

type simulateRelabelIn struct {
	OrgID         string              `json:"org_id" jsonschema:"the Shepherd org UUID"`
	Rules         []relabelRuleIn     `json:"rules"`
	SampleTargets []map[string]string `json:"sample_targets,omitempty" jsonschema:"label sets to trace; a built-in sample set is used if omitted"`
}

type simulateRelabelOut struct {
	Traces []TargetTraceView `json:"traces"`
}

func (b *Backend) simulateRelabel(ctx context.Context, _ *mcp.CallToolRequest, in simulateRelabelIn) (*mcp.CallToolResult, simulateRelabelOut, error) {
	rules := make([]*mgmtv1.RelabelRule, 0, len(in.Rules))
	for _, r := range in.Rules {
		rules = append(rules, &mgmtv1.RelabelRule{
			SourceLabels: r.SourceLabels, Separator: r.Separator, Regex: r.Regex,
			TargetLabel: r.TargetLabel, Replacement: r.Replacement, Action: r.Action, Modulus: r.Modulus,
		})
	}
	targets := make([]*structpb.Struct, 0, len(in.SampleTargets))
	for _, t := range in.SampleTargets {
		st, err := structpb.NewStruct(stringMapToAny(t))
		if err != nil {
			return nil, simulateRelabelOut{}, fmt.Errorf("sample_targets: %w", err)
		}
		targets = append(targets, st)
	}
	resp, err := b.Simulate.SimulateRelabel(ctx, connect.NewRequest(&mgmtv1.SimulateRelabelRequest{
		OrgId: in.OrgID, Rules: rules, SampleTargets: targets,
	}))
	if err != nil {
		return nil, simulateRelabelOut{}, err
	}
	return nil, simulateRelabelOut{Traces: toTargetTraceViews(resp.Msg.GetTraces())}, nil
}

func stringMapToAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// -- simulate_logs --

type stageSpecIn struct {
	Type        string            `json:"type" jsonschema:"json, logfmt, regex, labels, label_drop, drop, multiline, or template"`
	Expressions map[string]string `json:"expressions,omitempty"`
	Source      string            `json:"source,omitempty"`
	Separator   string            `json:"separator,omitempty"`
	Expression  string            `json:"expression,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	DropLabels  []string          `json:"drop_labels,omitempty"`
	DropValue   string            `json:"drop_value,omitempty"`
	Template    string            `json:"template,omitempty"`
	FirstLine   string            `json:"firstline,omitempty"`
}

type simulateLogsIn struct {
	OrgID       string        `json:"org_id" jsonschema:"the Shepherd org UUID"`
	Stages      []stageSpecIn `json:"stages"`
	SampleLines []string      `json:"sample_lines,omitempty" jsonschema:"lines to trace; a built-in sample set is used if omitted"`
}

type simulateLogsOut struct {
	Traces []LineTraceView `json:"traces"`
}

func (b *Backend) simulateLogs(ctx context.Context, _ *mcp.CallToolRequest, in simulateLogsIn) (*mcp.CallToolResult, simulateLogsOut, error) {
	stages := make([]*mgmtv1.StageSpec, 0, len(in.Stages))
	for i := range in.Stages {
		s := &in.Stages[i]
		stages = append(stages, &mgmtv1.StageSpec{
			Type: s.Type, Expressions: s.Expressions, Source: s.Source, Separator: s.Separator,
			Expression: s.Expression, Labels: s.Labels, DropLabels: s.DropLabels, DropValue: s.DropValue,
			Template: s.Template, Firstline: s.FirstLine,
		})
	}
	resp, err := b.Simulate.SimulateLogs(ctx, connect.NewRequest(&mgmtv1.SimulateLogsRequest{
		OrgId: in.OrgID, Stages: stages, SampleLines: in.SampleLines,
	}))
	if err != nil {
		return nil, simulateLogsOut{}, err
	}
	return nil, simulateLogsOut{Traces: toLineTraceViews(resp.Msg.GetTraces())}, nil
}

// -- create_sandbox_run / get_sandbox_run --

type createSandboxRunIn struct {
	OrgID           string          `json:"org_id" jsonschema:"the Shepherd org UUID"`
	Graph           json.RawMessage `json:"graph" jsonschema:"an alloy-graph/v1 GraphDocument, as stored in a visual pipeline's wizard_state (see get_pipeline)"`
	DurationSeconds int32           `json:"duration_seconds,omitempty" jsonschema:"0 selects the server default (30s), clamped to [1,120]"`
}

type createSandboxRunOut struct {
	RunID string `json:"run_id"`
}

func (b *Backend) createSandboxRun(ctx context.Context, _ *mcp.CallToolRequest, in createSandboxRunIn) (*mcp.CallToolResult, createSandboxRunOut, error) {
	graph := &mgmtv1.GraphDocument{}
	if err := protojson.Unmarshal(in.Graph, graph); err != nil {
		return nil, createSandboxRunOut{}, fmt.Errorf("graph is not a valid alloy-graph/v1 document: %w", err)
	}
	resp, err := b.Simulate.CreateRun(ctx, connect.NewRequest(&mgmtv1.CreateRunRequest{
		OrgId: in.OrgID, Graph: graph, DurationSeconds: in.DurationSeconds,
	}))
	if err != nil {
		return nil, createSandboxRunOut{}, err
	}
	return nil, createSandboxRunOut{RunID: resp.Msg.GetRunId()}, nil
}

func (b *Backend) getSandboxRun(ctx context.Context, _ *mcp.CallToolRequest, in orgIDArg) (*mcp.CallToolResult, SimulateRunView, error) {
	resp, err := b.Simulate.GetRun(ctx, connect.NewRequest(&mgmtv1.GetRunRequest{OrgId: in.OrgID, Id: in.ID}))
	if err != nil {
		return nil, SimulateRunView{}, err
	}
	return nil, toSimulateRunView(resp.Msg), nil
}
