package mcp

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// This file converts shepherd.mgmt.v1 proto response messages into plain Go
// "view" structs for tool output. This is translation, not policy: every
// field here is copied verbatim from the proto response Backend already
// received from mgmtapi (no field is computed, filtered for meaning, or
// second-guessed) — the sole reason for a separate type is that
// google.protobuf.Timestamp does not marshal to readable JSON via
// encoding/json (which is what the MCP SDK's AddTool uses for a tool's Out
// type), so timestamps are rendered as RFC3339 strings here instead of
// {seconds, nanos}.

// ts renders a possibly-nil proto timestamp as RFC3339, or "" when absent
// (mirrors internal/mgmtapi/rpc_gitops.go's protoTimestamp nil handling).
func ts(t *timestamppb.Timestamp) string {
	if t == nil {
		return ""
	}
	return t.AsTime().UTC().Format(time.RFC3339)
}

// CollectorView is FleetService's Collector, timestamp-rendered.
type CollectorView struct {
	ID                 string                  `json:"id"`
	ClusterID          string                  `json:"cluster_id"`
	Cluster            string                  `json:"cluster"`
	Role               string                  `json:"role"`
	RemoteConfigStatus string                  `json:"remote_config_status"`
	RemoteConfigError  string                  `json:"remote_config_error,omitempty"`
	LastSeen           string                  `json:"last_seen,omitempty"`
	AlloyVersion       string                  `json:"alloy_version,omitempty"`
	LocalAttributes    map[string]any          `json:"local_attributes,omitempty"`
	Instances          []CollectorInstanceView `json:"instances,omitempty"`
}

// CollectorInstanceView mirrors mgmtv1.CollectorInstance.
type CollectorInstanceView struct {
	Name               string `json:"name"`
	AlloyVersion       string `json:"alloy_version,omitempty"`
	OS                 string `json:"os,omitempty"`
	LastSeen           string `json:"last_seen,omitempty"`
	RemoteConfigStatus string `json:"remote_config_status"`
	RemoteConfigError  string `json:"remote_config_error,omitempty"`
}

func toCollectorView(c *mgmtv1.Collector) CollectorView {
	if c == nil {
		return CollectorView{}
	}
	v := CollectorView{
		ID: c.GetId(), ClusterID: c.GetClusterId(), Cluster: c.GetCluster(), Role: c.GetRole(),
		RemoteConfigStatus: c.GetRemoteConfigStatus(), RemoteConfigError: c.GetRemoteConfigError(),
		LastSeen: ts(c.GetLastSeen()), AlloyVersion: c.GetAlloyVersion(),
	}
	if la := c.GetLocalAttributes(); la != nil {
		v.LocalAttributes = la.AsMap()
	}
	for _, ci := range c.GetInstances() {
		v.Instances = append(v.Instances, CollectorInstanceView{
			Name: ci.GetName(), AlloyVersion: ci.GetAlloyVersion(), OS: ci.GetOs(),
			LastSeen: ts(ci.GetLastSeen()), RemoteConfigStatus: ci.GetRemoteConfigStatus(),
			RemoteConfigError: ci.GetRemoteConfigError(),
		})
	}
	return v
}

// PipelineView is PipelineService's Pipeline, timestamp-rendered.
type PipelineView struct {
	ID          string                 `json:"id"`
	OrgID       string                 `json:"org_id"`
	Name        string                 `json:"name"`
	Contents    string                 `json:"contents"`
	Matchers    []string               `json:"matchers"`
	Enabled     bool                   `json:"enabled"`
	Source      string                 `json:"source"`
	CreatedBy   string                 `json:"created_by,omitempty"`
	UpdatedBy   string                 `json:"updated_by,omitempty"`
	CreatedAt   string                 `json:"created_at,omitempty"`
	UpdatedAt   string                 `json:"updated_at,omitempty"`
	OwnerTeamID string                 `json:"owner_team_id,omitempty"`
	Revisions   []PipelineRevisionView `json:"revisions,omitempty"`
}

// PipelineRevisionView mirrors mgmtv1.PipelineRevision.
type PipelineRevisionView struct {
	Revision   int32  `json:"revision"`
	ChangedBy  string `json:"changed_by"`
	ChangedAt  string `json:"changed_at,omitempty"`
	ChangeNote string `json:"change_note,omitempty"`
}

func toPipelineView(p *mgmtv1.Pipeline) PipelineView {
	if p == nil {
		return PipelineView{}
	}
	v := PipelineView{
		ID: p.GetId(), OrgID: p.GetOrgId(), Name: p.GetName(), Contents: p.GetContents(),
		Matchers: p.GetMatchers(), Enabled: p.GetEnabled(), Source: p.GetSource(),
		CreatedBy: p.GetCreatedBy(), UpdatedBy: p.GetUpdatedBy(),
		CreatedAt: ts(p.GetCreatedAt()), UpdatedAt: ts(p.GetUpdatedAt()),
		OwnerTeamID: p.GetOwnerTeamId(),
	}
	for _, r := range p.GetRevisions() {
		v.Revisions = append(v.Revisions, PipelineRevisionView{
			Revision: r.GetRevision(), ChangedBy: r.GetChangedBy(),
			ChangedAt: ts(r.GetChangedAt()), ChangeNote: r.GetChangeNote(),
		})
	}
	return v
}

// DiagnosticView mirrors mgmtv1.Diagnostic.
type DiagnosticView struct {
	Line    int32  `json:"line"`
	Col     int32  `json:"col"`
	Message string `json:"message"`
	Stage   int32  `json:"stage"`
}

func toDiagnosticViews(ds []*mgmtv1.Diagnostic) []DiagnosticView {
	out := make([]DiagnosticView, 0, len(ds))
	for _, d := range ds {
		out = append(out, DiagnosticView{Line: d.GetLine(), Col: d.GetCol(), Message: d.GetMessage(), Stage: d.GetStage()})
	}
	return out
}

// MatchedCollectorView mirrors mgmtv1.MatchedCollector.
type MatchedCollectorView struct {
	Cluster string `json:"cluster"`
	Role    string `json:"role"`
	ID      string `json:"id"`
}

func toMatchedCollectorViews(ms []*mgmtv1.MatchedCollector) []MatchedCollectorView {
	out := make([]MatchedCollectorView, 0, len(ms))
	for _, m := range ms {
		out = append(out, MatchedCollectorView{Cluster: m.GetCluster(), Role: m.GetRole(), ID: m.GetId()})
	}
	return out
}

// SimulateRunView is a trimmed SimulateRun (S3 sandbox run) for tool output.
type SimulateRunView struct {
	ID                       string                     `json:"id"`
	Status                   string                     `json:"status"`
	CreatedAt                string                     `json:"created_at,omitempty"`
	StartedAt                string                     `json:"started_at,omitempty"`
	FinishedAt               string                     `json:"finished_at,omitempty"`
	RequestedDurationSeconds int32                      `json:"requested_duration_seconds"`
	QueuePosition            int32                      `json:"queue_position,omitempty"`
	CapturedSeriesCount      int                        `json:"captured_series_count"`
	CapturedLogLineCount     int                        `json:"captured_log_line_count"`
	ComponentHealth          []ComponentHealthView      `json:"component_health,omitempty"`
	GateDiagnostics          []VisualNodeDiagnosticView `json:"gate_diagnostics,omitempty"`
	StderrTail               string                     `json:"stderr_tail,omitempty"`
	ErrorCode                string                     `json:"error_code,omitempty"`
	ErrorMessage             string                     `json:"error_message,omitempty"`
	FidelityNote             string                     `json:"fidelity_note"`
}

// ComponentHealthView mirrors mgmtv1.ComponentHealth.
type ComponentHealthView struct {
	NodeID      string `json:"node_id"`
	NodeLabel   string `json:"node_label"`
	Component   string `json:"component"`
	HealthState string `json:"health_state"`
	Message     string `json:"message,omitempty"`
}

// VisualNodeDiagnosticView mirrors mgmtv1.VisualNodeDiagnostic.
type VisualNodeDiagnosticView struct {
	Layer   string `json:"layer"`
	NodeID  string `json:"node_id"`
	Line    int32  `json:"line,omitempty"`
	Col     int32  `json:"col,omitempty"`
	Message string `json:"message"`
}

func toSimulateRunView(r *mgmtv1.SimulateRun) SimulateRunView {
	v := SimulateRunView{
		ID: r.GetId(), Status: r.GetStatus(), CreatedAt: ts(r.GetCreatedAt()),
		StartedAt: ts(r.GetStartedAt()), FinishedAt: ts(r.GetFinishedAt()),
		RequestedDurationSeconds: r.GetRequestedDurationSeconds(), QueuePosition: r.GetQueuePosition(),
		CapturedSeriesCount: len(r.GetCapturedSeries()), CapturedLogLineCount: len(r.GetCapturedLogLines()),
		StderrTail: r.GetStderrTail(), ErrorCode: r.GetErrorCode(), ErrorMessage: r.GetErrorMessage(),
		FidelityNote: r.GetFidelityNote(),
	}
	for _, h := range r.GetComponentHealth() {
		v.ComponentHealth = append(v.ComponentHealth, ComponentHealthView{
			NodeID: h.GetNodeId(), NodeLabel: h.GetNodeLabel(), Component: h.GetComponent(),
			HealthState: h.GetHealthState(), Message: h.GetMessage(),
		})
	}
	for _, d := range r.GetGateDiagnostics() {
		v.GateDiagnostics = append(v.GateDiagnostics, VisualNodeDiagnosticView{
			Layer: d.GetLayer(), NodeID: d.GetNodeId(), Line: d.GetLine(), Col: d.GetCol(), Message: d.GetMessage(),
		})
	}
	return v
}

// TargetTraceView mirrors mgmtv1.TargetTrace (SimulateRelabel).
type TargetTraceView struct {
	Input  map[string]string `json:"input"`
	Steps  []RelabelStepView `json:"steps"`
	Output map[string]string `json:"output"`
	Kept   bool              `json:"kept"`
}

// RelabelStepView mirrors mgmtv1.RelabelStep.
type RelabelStepView struct {
	RuleIndex int32             `json:"rule_index"`
	Action    string            `json:"action"`
	Before    map[string]string `json:"before"`
	After     map[string]string `json:"after"`
	Kept      bool              `json:"kept"`
}

func toTargetTraceViews(ts []*mgmtv1.TargetTrace) []TargetTraceView {
	out := make([]TargetTraceView, 0, len(ts))
	for _, t := range ts {
		var steps []RelabelStepView
		for _, s := range t.GetSteps() {
			steps = append(steps, RelabelStepView{
				RuleIndex: s.GetRuleIndex(), Action: s.GetAction(), Before: s.GetBefore(), After: s.GetAfter(), Kept: s.GetKept(),
			})
		}
		out = append(out, TargetTraceView{Input: t.GetInput(), Steps: steps, Output: t.GetOutput(), Kept: t.GetKept()})
	}
	return out
}

// LineTraceView mirrors mgmtv1.LineTrace (SimulateLogs).
type LineTraceView struct {
	Input   string            `json:"input"`
	Steps   []StageEffectView `json:"steps"`
	Output  string            `json:"output"`
	Dropped bool              `json:"dropped"`
}

// StageEffectView mirrors mgmtv1.StageEffect.
type StageEffectView struct {
	StageIndex   int32             `json:"stage_index"`
	StageType    string            `json:"stage_type"`
	Simulated    bool              `json:"simulated"`
	LineBefore   string            `json:"line_before"`
	LineAfter    string            `json:"line_after,omitempty"`
	LabelsBefore map[string]string `json:"labels_before,omitempty"`
	LabelsAfter  map[string]string `json:"labels_after,omitempty"`
	Dropped      bool              `json:"dropped"`
	Note         string            `json:"note,omitempty"`
}

func toLineTraceViews(ls []*mgmtv1.LineTrace) []LineTraceView {
	out := make([]LineTraceView, 0, len(ls))
	for _, l := range ls {
		var steps []StageEffectView
		for _, s := range l.GetSteps() {
			steps = append(steps, StageEffectView{
				StageIndex: s.GetStageIndex(), StageType: s.GetStageType(), Simulated: s.GetSimulated(),
				LineBefore: s.GetLineBefore(), LineAfter: s.GetLineAfter(),
				LabelsBefore: s.GetLabelsBefore(), LabelsAfter: s.GetLabelsAfter(),
				Dropped: s.GetDropped(), Note: s.GetNote(),
			})
		}
		out = append(out, LineTraceView{Input: l.GetInput(), Steps: steps, Output: l.GetOutput(), Dropped: l.GetDropped()})
	}
	return out
}
