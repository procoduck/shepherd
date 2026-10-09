// Package validate implements the 3-stage validation gate for Alloy pipeline content.
//
// Stage 1: Syntax parsing via github.com/grafana/alloy/syntax/parser.
// Stage 2: Semantic validation — an in-process port-shape check against the
// component schema (PortShapes, #233), then exec of the bundled alloy binary.
// Stage 3: Merge dry-run — validate every affected collector's merged config.
package validate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/alloy/syntax/diag"
	"github.com/grafana/alloy/syntax/parser"

	"shepherd/internal/config"
	"shepherd/internal/metrics"
)

// Diagnostic is a structured error from any validation stage.
type Diagnostic struct {
	// Line is the 1-based line number (0 if unknown).
	Line int `json:"line"`
	// Col is the 1-based column number (0 if unknown).
	Col int `json:"col"`
	// Message is the human-readable error text.
	Message string `json:"message"`
	// Stage identifies which validation stage produced this diagnostic (1, 2, or 3).
	Stage int `json:"stage"`
}

// Result is returned by all validation functions.
type Result struct {
	// Valid is true when no errors were found.
	Valid bool
	// Diagnostics holds all errors and warnings.
	Diagnostics []Diagnostic
	// Skipped lists the stages that did not run (today only 2, when no
	// Alloy binary is configured). Valid=true with a non-empty Skipped means
	// "nothing the stages that ran could find" — not that the content passed
	// every stage (#209). A stage that was never reached because an earlier
	// one failed is not listed: that is a failure, not a skip.
	Skipped []int
}

// Validator holds configuration for running validation.
type Validator struct {
	alloyBinary    string
	stabilityLevel string
	timeout        time.Duration
	stage3Timeout  time.Duration
}

// New creates a Validator from config.
func New(cfg *config.ValidateConfig) *Validator {
	s3t := cfg.Stage3Timeout
	if s3t == 0 {
		s3t = 30 * time.Second
	}
	return &Validator{
		alloyBinary:    cfg.AlloyBinary,
		stabilityLevel: cfg.StabilityLevel,
		timeout:        cfg.Timeout,
		stage3Timeout:  s3t,
	}
}

// Stage3Timeout returns the budget for Stage 3 validation.
func (v *Validator) Stage3Timeout() time.Duration { return v.stage3Timeout }

// Stage1 parses the Alloy syntax and returns structured diagnostics.
// content should be the raw pipeline body (not yet declare-wrapped).
func Stage1(content string) (res Result) {
	// Recorded on every return path via defer rather than at each `return`:
	// Stage1 has four of them, and a counter that four call sites have to
	// remember is a counter that eventually misses one.
	defer func() { metrics.ObserveValidation("1", res.Valid) }()

	_, err := parser.ParseFile("<pipeline>", []byte(content))
	if err == nil {
		return Result{Valid: true}
	}

	var diags diag.Diagnostics
	ok := errors.As(err, &diags)
	if !ok {
		return Result{Diagnostics: []Diagnostic{{Line: 1, Col: 1, Message: err.Error(), Stage: 1}}}
	}

	var out []Diagnostic
	for _, d := range diags {
		out = append(out, Diagnostic{
			Line:    d.StartPos.Line,
			Col:     d.StartPos.Column,
			Message: d.Message,
			Stage:   1,
		})
	}
	return Result{Diagnostics: out}
}

// Stage2 runs the semantic checks on the given content (declare-wrapped as it
// will be served) and returns structured diagnostics:
//
//  1. PortShapes — in-process, against the embedded component schema. It
//     catches wires `alloy validate` accepts but Alloy refuses at load (a
//     targets list wrapped in another list, a single receiver where a list is
//     required). It needs no binary, so it runs even when (2) is skipped.
//  2. `alloy validate` via the bundled binary. If AlloyBinary is empty, this
//     half is skipped and the result carries Skipped=[2] (#209), so callers
//     can tell the operator it never ran.
//
// Diagnostics from both are returned together, so one save reports everything.
func (v *Validator) Stage2(ctx context.Context, content string) (res Result) {
	shapes := PortShapes(content)
	if v.alloyBinary == "" {
		// Counted as "skipped", not "valid". The distinction is the whole
		// point: v0.0.2 shipped an image where `alloy validate` could not run
		// at all, and every deployment silently skipped this stage while
		// reporting success. A dashboard where stage 2 is 100% skipped in
		// production is that bug, visible — and Skipped carries the same
		// distinction to the API response (#209). A port-shape refusal is a
		// real stage-2 verdict, though, and is counted as one; `alloy validate`
		// still did not run, so the result says so.
		if len(shapes) > 0 {
			metrics.ObserveValidation("2", false)
			return Result{Diagnostics: shapes, Skipped: []int{2}}
		}
		metrics.ValidationTotal.WithLabelValues("2", "skipped").Inc()
		return Result{Valid: true, Skipped: []int{2}}
	}
	defer func() { metrics.ObserveValidation("2", res.Valid) }()

	res = v.alloyValidate(ctx, content)
	if len(shapes) > 0 {
		res.Valid = false
		res.Diagnostics = append(res.Diagnostics, shapes...)
	}
	return res
}

// alloyValidate execs `alloy validate` on content.
func (v *Validator) alloyValidate(ctx context.Context, content string) Result {
	tmp, err := os.CreateTemp("", "shepherd-validate-*.alloy")
	if err != nil {
		return Result{Diagnostics: []Diagnostic{{Line: 1, Message: fmt.Sprintf("creating temp file: %v", err), Stage: 2}}}
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort temp file cleanup
	if _, err := tmp.WriteString(content); err != nil {
		return Result{Diagnostics: []Diagnostic{{Line: 1, Message: fmt.Sprintf("writing temp file: %v", err), Stage: 2}}}
	}
	if err := tmp.Close(); err != nil {
		return Result{Diagnostics: []Diagnostic{{Line: 1, Message: fmt.Sprintf("closing temp file: %v", err), Stage: 2}}}
	}

	tCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	//nolint:gosec // alloy binary path comes from config, not user input
	cmd := exec.CommandContext(tCtx, v.alloyBinary, "validate",
		"--stability.level="+v.stabilityLevel, tmp.Name())
	out, err := cmd.CombinedOutput()
	if err == nil {
		return Result{Valid: true}
	}

	return Result{Diagnostics: parseAlloyOutput(string(out))}
}

// Stages12 runs stage 1 then stage 2. Returns early if stage 1 fails.
// content should be the declare-wrapped pipeline content (as it will be served).
func (v *Validator) Stages12(ctx context.Context, content string) Result {
	r1 := Stage1(content)
	if !r1.Valid && len(r1.Diagnostics) > 0 {
		return r1
	}
	return v.Stage2(ctx, content)
}

// ValidatePipeline runs Stages 1 and 2 on a pipeline's contents exactly as
// they will be served — declare-wrapped by WrapForValidation — and reports
// every diagnostic in the coordinates of contents, the text the user wrote
// (UnwrapDiagnostics). It is the one entry point for any caller that shows a
// pipeline's diagnostics to a person: the ValidatePipeline RPC (and the MCP
// tools composed on it), pipeline create/update, the wizard render/commit
// gate, the destination re-render and repo sync. Validating the wrapped text
// and reporting its positions as-is put every diagnostic one line down and
// two columns right (walkthrough F2).
//
// Only the positions change: the verdict, Skipped and every message are
// exactly those of Stages12 on the wrapped document.
func (v *Validator) ValidatePipeline(ctx context.Context, pipelineName, contents string) Result {
	r := v.Stages12(ctx, WrapForValidation(pipelineName, contents))
	r.Diagnostics = UnwrapDiagnostics(contents, r.Diagnostics)
	return r
}

// wrapHeaderLines is how many lines WrapForValidation writes before the first
// line of the contents, and wrapIndent the indent it adds to every non-empty
// contents line. UnwrapDiagnostics inverts exactly that layout; its specs pin
// the two together.
const (
	wrapHeaderLines = 1
	wrapIndent      = 2
)

// UnwrapDiagnostics maps diagnostics positioned in
// WrapForValidation(_, contents) back to contents:
//
//   - a line of contents moves up by the header; its column moves left by the
//     indent when WrapForValidation indented that line (every non-empty one).
//     A column inside the indent itself clamps to 1.
//   - the header line maps to the start of contents (1:1).
//   - a position in the wrapper's own trailing lines — the closing brace an
//     unterminated block runs into, the instantiation, end of file — clamps
//     to the end of contents with its message kept: that is where the text
//     the user wrote stopped being what the parser needed.
//   - an unknown line or column (0) stays unknown.
//
// The input slice is not modified; nil in, nil out.
func UnwrapDiagnostics(contents string, diags []Diagnostic) []Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	lines := strings.Split(contents, "\n")
	lastLine := len(lines)
	endCol := len(lines[lastLine-1]) + 1

	out := make([]Diagnostic, len(diags))
	for i, d := range diags {
		switch {
		case d.Line <= 0:
			// Unknown position: nothing to map.
		case d.Line <= wrapHeaderLines:
			d.Line = 1
			if d.Col > 0 {
				d.Col = 1
			}
		case d.Line-wrapHeaderLines <= lastLine:
			d.Line -= wrapHeaderLines
			if lines[d.Line-1] != "" && d.Col > 0 {
				d.Col = max(1, d.Col-wrapIndent)
			}
		default:
			d.Line = lastLine
			if d.Col > 0 {
				d.Col = endCol
			}
		}
		out[i] = d
	}
	return out
}

// WrapForValidation wraps raw pipeline contents in the same declare block the
// merge engine uses, so Stage 2 validates exactly what will be served.
func WrapForValidation(pipelineName, contents string) string {
	blockName := "pipe_" + sanitizeName(pipelineName)
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "declare %q {\n", blockName)
	for _, line := range strings.Split(contents, "\n") {
		if line == "" {
			sb.WriteString("\n")
		} else {
			sb.WriteString("  " + line + "\n")
		}
	}
	sb.WriteString("}\n")
	_, _ = fmt.Fprintf(&sb, "%s \"default\" { }\n", blockName)
	return sb.String()
}

// sanitizeRe matches characters outside [a-z0-9_].
var sanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

func sanitizeName(name string) string {
	r := sanitizeRe.ReplaceAllString(strings.ToLower(name), "_")
	if len(r) == 0 || (r[0] >= '0' && r[0] <= '9') {
		r = "p" + r
	}
	return r
}

// stderrLineRe parses lines like "file.alloy:10:5: error message" from alloy validate output.
var stderrLineRe = regexp.MustCompile(`(?m)^.+:(\d+):(\d+):\s*(.+)$`)

func parseAlloyOutput(output string) []Diagnostic {
	matches := stderrLineRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		// No structured lines — attach to line 1.
		msg := strings.TrimSpace(output)
		if msg == "" {
			msg = "alloy validate failed (no output)"
		}
		return []Diagnostic{{Line: 1, Col: 1, Message: msg, Stage: 2}}
	}

	var out []Diagnostic
	seen := map[string]bool{}
	for _, m := range matches {
		key := m[1] + ":" + m[2] + ":" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		// Parsed with bitSize 32 so the int32 the proto carries can never be
		// narrowed from a wider value (CodeQL go/incorrect-integer-conversion);
		// 0 is the safe fallback for a number the regex matched but that does
		// not fit.
		line, _ := strconv.ParseInt(m[1], 10, 32) //nolint:errcheck // 0 is safe fallback
		col, _ := strconv.ParseInt(m[2], 10, 32)  //nolint:errcheck // 0 is safe fallback
		out = append(out, Diagnostic{Line: int(line), Col: int(col), Message: strings.TrimSpace(m[3]), Stage: 2})
	}
	return out
}
