package mgmtapi

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/simulate"
)

// SimulateService implements mgmtv1connect.SimulateServiceHandler — the
// business logic for /api/orgs/{org}/simulate/*, moved here from
// SimulateHandler (simulate.go, now a thin REST shim over this service).
type SimulateService struct {
	logger *slog.Logger
}

// NewSimulateService constructs a SimulateService with the deps SimulateHandler uses today.
func NewSimulateService(logger *slog.Logger) *SimulateService {
	return &SimulateService{logger: logger}
}

var _ mgmtv1connect.SimulateServiceHandler = (*SimulateService)(nil)

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
