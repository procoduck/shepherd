package simulate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// isDeadlineErr reports whether err is (or wraps) a context deadline —
// either the run's overall RunTTL budget or a poll's own ctx.
func isDeadlineErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// decodeSchemaPayload converts the schema registry's merged map[string]any
// into visual.SchemaPayload via a JSON round-trip — the same conversion
// rpc_visual.go's loadSchemaPayload performs, duplicated here because
// internal/simulate cannot import internal/mgmtapi (mgmtapi already imports
// internal/simulate).
func decodeSchemaPayload(merged map[string]any) (visual.SchemaPayload, error) {
	b, err := json.Marshal(merged)
	if err != nil {
		return visual.SchemaPayload{}, err
	}
	var payload visual.SchemaPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return visual.SchemaPayload{}, err
	}
	return payload, nil
}

// renderDiagnosticsToGate converts L1 render diagnostics into the
// gate_diagnostics shape. Render diagnostics carry no line/col (they are
// found before any Alloy text exists), so Line/Col are left zero.
func renderDiagnosticsToGate(diags []visual.RenderDiagnostic) []RunGateDiagnostic {
	out := make([]RunGateDiagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, RunGateDiagnostic{Layer: "L1", NodeID: d.NodeID, Message: d.Message})
	}
	return out
}

// validateDiagnosticsToGate converts Stage1/2 diagnostics into
// gate_diagnostics, resolving each diagnostic's line back to the node whose
// rendered range contains it — the same lookup rpc_visual.go's Validate uses.
func validateDiagnosticsToGate(diags []validate.Diagnostic, nodeMap map[string]visual.NodeRange) []RunGateDiagnostic {
	out := make([]RunGateDiagnostic, 0, len(diags))
	for _, d := range diags {
		id := ""
		for node, rg := range nodeMap {
			if d.Line >= rg.StartLine && d.Line <= rg.EndLine {
				id = node
				break
			}
		}
		out = append(out, RunGateDiagnostic{Layer: "L2", NodeID: id, Line: d.Line, Col: d.Col, Message: d.Message})
	}
	return out
}

// buildRunInputs derives the two pieces of the sandbox request that depend
// on the graph shape: component_index (rendered Alloy local id -> graph node
// id, for every node the transformed graph actually rendered) and
// log_fixtures (every loki_file stub's fixture name, read from the
// AUTHORED graph: the transform rewrites node.Component to the stub
// component, so only the pre-transform component still names the original
// policy entry).
func buildRunInputs(original, transformed visual.GraphDocument, policy Policy, nodeMap map[string]visual.NodeRange) (map[string]string, []string) {
	byID := make(map[string]visual.GraphNode, len(transformed.Nodes))
	for _, n := range transformed.Nodes {
		byID[n.ID] = n
	}
	index := make(map[string]string, len(nodeMap))
	for id := range nodeMap {
		n, ok := byID[id]
		if !ok {
			continue
		}
		index[n.Component+"."+visual.SanitizeLabel(n.Label)] = id
	}

	seen := map[string]bool{}
	var fixtures []string
	for _, n := range original.Nodes {
		if n.Disabled {
			continue
		}
		cp, ok := policy.Components[n.Component]
		if !ok || cp.Stub == nil || cp.Stub.Type != StubTypeLokiFile {
			continue
		}
		if !seen[cp.Stub.Fixture] {
			seen[cp.Stub.Fixture] = true
			fixtures = append(fixtures, cp.Stub.Fixture)
		}
	}
	return index, fixtures
}

// originalNodeInfo is what component-health conversion needs from the
// authored graph: the label/component a node id should be displayed with,
// which the transform may have overwritten on the transformed copy.
type originalNodeInfo struct {
	Label     string
	Component string
}

func indexOriginalNodes(doc visual.GraphDocument) map[string]originalNodeInfo {
	out := make(map[string]originalNodeInfo, len(doc.Nodes))
	for _, n := range doc.Nodes {
		out[n.ID] = originalNodeInfo{Label: n.Label, Component: n.Component}
	}
	return out
}

func toRunSeries(in []ClientSeries) []RunSeries {
	out := make([]RunSeries, 0, len(in))
	for _, s := range in {
		out = append(out, RunSeries{Name: s.Name, Labels: s.Labels, SampleCount: s.SampleCount})
	}
	return out
}

func toRunLogLines(in []ClientLogLine) []RunLogLine {
	out := make([]RunLogLine, 0, len(in))
	for _, l := range in {
		out = append(out, RunLogLine{Labels: l.Labels, Line: l.Line})
	}
	return out
}

func toRunComponentHealth(in []ClientComponentHealth, nodeInfo map[string]originalNodeInfo) []RunComponentHealth {
	out := make([]RunComponentHealth, 0, len(in))
	for _, c := range in {
		info := nodeInfo[c.NodeID]
		out = append(out, RunComponentHealth{
			NodeID: c.NodeID, NodeLabel: info.Label, Component: info.Component,
			HealthState: c.Health, Message: c.Message,
		})
	}
	return out
}

func joinStderr(lines []string) string {
	return strings.Join(lines, "\n")
}

// maxStderrTailBytes caps the persisted stderr tail — an unbounded capture
// becomes an unbounded JSONB/TEXT row (run-API spec decision 15).
const maxStderrTailBytes = 8 * 1024

func capStderr(s string) string {
	if len(s) <= maxStderrTailBytes {
		return s
	}
	return s[len(s)-maxStderrTailBytes:]
}

// marshalOrEmptyArray marshals v, falling back to an empty JSON array on
// error (matching the simulate_runs columns' NOT NULL DEFAULT '[]') rather
// than writing a NULL a nil slice would otherwise produce.
func marshalOrEmptyArray(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || b == nil || string(b) == "null" {
		return json.RawMessage("[]")
	}
	return b
}

// classifySimulatorError maps a Client error into one of the closed
// SimulateRun.error_code values, per the run-API spec's error-mapping table.
func classifySimulatorError(err error) (code, message string) {
	var apiErr *ClientAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "queue_full", "shutting_down":
			return RunErrorSimulatorUnavailable, apiErr.Message
		case "invalid_config", "endpoint_not_allowed", "config_too_large":
			// Shepherd renders and validates the config before ever sending
			// it; the simulator rejecting it is a Shepherd-side bug, not a
			// user-actionable condition.
			return RunErrorInternal, apiErr.Message
		default:
			return RunErrorInternal, apiErr.Message
		}
	}
	if errors.Is(err, ErrSimulatorUnreachable) {
		return RunErrorSimulatorUnavailable, err.Error()
	}
	if isDeadlineErr(err) {
		return RunErrorTimeout, err.Error()
	}
	return RunErrorInternal, err.Error()
}
