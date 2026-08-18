// Package validate implements the 3-stage validation gate for Alloy pipeline content.
//
// Stage 1: Syntax parsing via github.com/grafana/alloy/syntax/parser.
// Stage 2: Semantic validation via exec of the bundled alloy binary.
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
func Stage1(content string) Result {
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

// Stage2 runs `alloy validate` on the given content (declare-wrapped as it will
// be served) and returns structured diagnostics.
// If AlloyBinary is empty, Stage 2 is skipped and returns valid.
func (v *Validator) Stage2(ctx context.Context, content string) Result {
	if v.alloyBinary == "" {
		return Result{Valid: true}
	}
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
		line, _ := strconv.Atoi(m[1]) //nolint:errcheck // 0 is safe fallback
		col, _ := strconv.Atoi(m[2])  //nolint:errcheck // 0 is safe fallback
		out = append(out, Diagnostic{Line: line, Col: col, Message: strings.TrimSpace(m[3]), Stage: 2})
	}
	return out
}
