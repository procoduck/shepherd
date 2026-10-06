package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"shepherd/internal/simulate"
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
// internal/simulate/worker cannot import internal/mgmtapi (mgmtapi already
// imports internal/simulate).
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
func renderDiagnosticsToGate(diags []visual.RenderDiagnostic) []simulate.RunGateDiagnostic {
	out := make([]simulate.RunGateDiagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, simulate.RunGateDiagnostic{Layer: "L1", NodeID: d.NodeID, Message: d.Message})
	}
	return out
}

// validateDiagnosticsToGate converts Stage1/2 diagnostics into
// gate_diagnostics, resolving each diagnostic's line back to the node whose
// rendered range contains it — the same lookup rpc_visual.go's Validate uses.
func validateDiagnosticsToGate(diags []validate.Diagnostic, nodeMap map[string]visual.NodeRange) []simulate.RunGateDiagnostic {
	out := make([]simulate.RunGateDiagnostic, 0, len(diags))
	for _, d := range diags {
		id := ""
		for node, rg := range nodeMap {
			if d.Line >= rg.StartLine && d.Line <= rg.EndLine {
				id = node
				break
			}
		}
		out = append(out, simulate.RunGateDiagnostic{Layer: "L2", NodeID: id, Line: d.Line, Col: d.Col, Message: d.Message})
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
func buildRunInputs(original, transformed visual.GraphDocument, policy simulate.Policy, nodeMap map[string]visual.NodeRange) (map[string]string, []string) {
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
		if !ok || cp.Stub == nil || cp.Stub.Type != simulate.StubTypeLokiFile {
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

func toRunSeries(in []simulate.ClientSeries) []simulate.RunSeries {
	out := make([]simulate.RunSeries, 0, len(in))
	for _, s := range in {
		out = append(out, simulate.RunSeries{Name: s.Name, Labels: s.Labels, SampleCount: s.SampleCount})
	}
	return out
}

func toRunLogLines(in []simulate.ClientLogLine) []simulate.RunLogLine {
	out := make([]simulate.RunLogLine, 0, len(in))
	for _, l := range in {
		out = append(out, simulate.RunLogLine{Labels: l.Labels, Line: l.Line})
	}
	return out
}

// toRunComponentHealth converts the sandbox's per-component health into the
// stored shape. A node the transform replaced with a stub (rewrites of kind
// discovery_stubbed / log_source_stubbed) is reported as
// simulate.HealthStateStubbed rather than with the sandbox's state: what the
// sandbox measured is the stand-in (a discovery.relabel or loki.source.file),
// and "healthy" on the user's discovery.kubernetes node claimed something the
// run never tested (#253). The stand-in's own state stays in the message,
// since a stand-in that failed is still worth knowing about.
func toRunComponentHealth(in []simulate.ClientComponentHealth, nodeInfo map[string]originalNodeInfo, rewrites []simulate.Rewrite) []simulate.RunComponentHealth {
	stubbed := make(map[string]simulate.Rewrite)
	for i := range rewrites {
		if k := rewrites[i].Kind; k == simulate.RewriteDiscoveryStubbed || k == simulate.RewriteLogSourceStubbed {
			stubbed[rewrites[i].NodeID] = rewrites[i]
		}
	}
	out := make([]simulate.RunComponentHealth, 0, len(in))
	for _, c := range in {
		info := nodeInfo[c.NodeID]
		state, msg := c.Health, c.Message
		if rw, ok := stubbed[c.NodeID]; ok {
			state = simulate.HealthStateStubbed
			msg = fmt.Sprintf("stubbed: %s did not run in the sandbox (%s). The stand-in reported %q", info.Component, rw.Detail, c.Health)
			if c.Message != "" {
				msg += ": " + c.Message
			}
		}
		out = append(out, simulate.RunComponentHealth{
			NodeID: c.NodeID, NodeLabel: info.Label, Component: info.Component,
			HealthState: state, Message: msg,
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
	return SanitizeStderrTail(s)
}

// SanitizeStderrTail makes s safe to store in simulate_runs.stderr_tail, a
// Postgres TEXT column, and safe to render as a log tail: it strips ASCII
// control characters (other than newline and tab) and truncates to the last
// maxStderrTailBytes bytes without splitting a UTF-8 rune.
//
// The input is the sandbox Alloy's raw stderr, read line-by-line via
// bufio.Scanner.Text() (internal/simsvc/runner.go) — never validated as
// UTF-8 at that layer. A plain byte-index cut (s[len(s)-max:]) can start
// mid-rune, and Postgres rejects the resulting invalid UTF-8 with SQLSTATE
// 22021; CompleteSimulateRun then fails and the run sits until the janitor
// reaps it (worker.go's finish/CompleteSimulateRun call).
func SanitizeStderrTail(s string) string {
	s = strings.ToValidUTF8(stripControlChars(s), "")
	if len(s) <= maxStderrTailBytes {
		return s
	}
	cut := s[len(s)-maxStderrTailBytes:]
	// The byte-index cut lands on a valid rune boundary already (s is valid
	// UTF-8 throughout, courtesy of ToValidUTF8 above) UNLESS it fell inside
	// a multi-byte rune's trailing bytes — those are UTF-8 continuation
	// bytes (10xxxxxx), which utf8.RuneStart identifies. Skip past them
	// rather than keep a fragment that would decode as replacement
	// characters or, worse, as different runes than were ever written.
	for i := 0; i < len(cut) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(cut[i]) {
			return cut[i:]
		}
	}
	return cut
}

// stripControlChars removes ASCII control characters other than newline and
// tab — the ones a terminal-oriented log line accumulates (NUL, ANSI escape
// sequences, DEL) — while leaving every other rune, including non-ASCII
// text, untouched.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
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
	var apiErr *simulate.ClientAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "queue_full", "shutting_down":
			return simulate.RunErrorSimulatorUnavailable, apiErr.Message
		case "unauthorized":
			// A 401 here means the bearer token Shepherd is configured with
			// (SHEPHERD_SIMULATOR_TOKEN / config.simulator.token) doesn't
			// match the simulator's own SIM_TOKEN — a deployment/config
			// problem, not something the user's pipeline caused or can fix.
			// Reported as simulator_unavailable (retry-shaped, like the
			// simulator being unreachable) with a message that names the
			// actual cause instead of the bare "internal error" a user
			// cannot act on.
			return simulate.RunErrorSimulatorUnavailable,
				"simulator rejected the configured bearer token — check that config.simulator.token (or SHEPHERD_SIMULATOR_TOKEN) matches the simulator's SIM_TOKEN: " + apiErr.Message
		case "invalid_config", "endpoint_not_allowed", "config_too_large":
			// Shepherd renders and validates the config before ever sending
			// it; the simulator rejecting it is a Shepherd-side bug, not a
			// user-actionable condition.
			return simulate.RunErrorInternal, apiErr.Message
		default:
			return simulate.RunErrorInternal, apiErr.Message
		}
	}
	if errors.Is(err, simulate.ErrSimulatorUnreachable) {
		return simulate.RunErrorSimulatorUnavailable, err.Error()
	}
	if isDeadlineErr(err) {
		return simulate.RunErrorTimeout, err.Error()
	}
	return simulate.RunErrorInternal, err.Error()
}
