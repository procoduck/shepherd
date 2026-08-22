package wizard

import (
	"errors"
	"strings"
	"testing"

	"shepherd/internal/signals"
)

// metricsOnlyPipeline and logsOnlyPipeline are minimal, genuinely
// signal-shaped Alloy fragments — real component names/wire types checked
// against the pinned schema artifact by signals.Derive itself, not asserted
// from memory. Mirrors internal/merge/merge_test.go's fixtures of the same
// shape.
const metricsOnlyPipeline = `
prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`

const logsOnlyPipeline = `
loki.source.file "app" {
  targets = []
  forward_to = [loki.write.sink.receiver]
}

loki.write "sink" {
  endpoint {
    url = "https://example.com/loki/api/v1/push"
  }
}
`

func TestCheckRole(t *testing.T) {
	cases := []struct {
		name      string
		role      string
		contents  string
		wantErr   error // nil means checkRole must return nil
		wantInMsg string
	}{
		{"metrics role, metrics-only pipeline: allowed", "metrics", metricsOnlyPipeline, nil, ""},
		{"metrics role, logs-only pipeline: refused", "metrics", logsOnlyPipeline, signals.ErrSignalMismatch, `role "metrics"`},
		{"logs role, logs-only pipeline: allowed", "logs", logsOnlyPipeline, nil, ""},
		{"logs role, metrics-only pipeline: refused", "logs", metricsOnlyPipeline, signals.ErrSignalMismatch, `role "logs"`},
		{"singleton role, logs-only pipeline: allowed (unrestricted)", "singleton", logsOnlyPipeline, nil, ""},
		{"unknown role: refused regardless of content", "not-a-real-role", metricsOnlyPipeline, signals.ErrUnknownRole, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRole(tc.role, tc.contents)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("checkRole(%q, ...) = %v, want nil", tc.role, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("checkRole(%q, ...) = %v, want error wrapping %v", tc.role, err, tc.wantErr)
			}
			if tc.wantInMsg != "" && !strings.Contains(err.Error(), tc.wantInMsg) {
				t.Fatalf("checkRole error %q does not name the declared role %q", err.Error(), tc.wantInMsg)
			}
		})
	}
}

// fakeMismatchedWizard is a minimal Wizard whose Commit ALWAYS produces a
// logs-shaped pipeline while Role ALWAYS declares "metrics" — the exact
// contradiction G6 exists to catch (docs/gateway-tier-plan.md W8). It exists
// only to exercise Register's wrapping in isolation from any real catalog
// wizard's template logic.
type fakeMismatchedWizard struct{}

func (fakeMismatchedWizard) Kind() string               { return "test-fake-mismatched" }
func (fakeMismatchedWizard) Schema() Schema             { return Schema{Kind: "test-fake-mismatched"} }
func (fakeMismatchedWizard) Role(map[string]any) string { return "metrics" }
func (fakeMismatchedWizard) Commit(map[string]any) (CommitResult, error) {
	return CommitResult{Contents: logsOnlyPipeline}, nil
}

// TestRegisterRefusesRoleMismatch proves the property this file exists to
// prove: a wizard whose own Commit would happily return a role-mismatched
// pipeline is refused anyway, because Register (not the wizard) is what
// decides whether Commit's result reaches the caller.
//
// Red run, executed: commenting out the `if checkErr := checkRole(...)`
// block in role.go's roleEnforced.Commit (leaving `result.Role = role;
// return result, nil` unconditional) and rerunning this test fails it —
//
//	registry.Get("test-fake-mismatched").Commit(nil) = {Contents: "loki.source.file...", Role: "metrics"}, <nil>, want a signals.ErrSignalMismatch error
//
// i.e. the mismatched pipeline is served with no error and role "metrics"
// stamped on it, exactly the silent-mismatch failure class G6 exists to
// close. The block was restored immediately after observing that failure.
func TestRegisterRefusesRoleMismatch(t *testing.T) {
	reg := &Registry{wizards: make(map[string]Wizard)}
	reg.wizards[fakeMismatchedWizard{}.Kind()] = roleEnforced{Wizard: fakeMismatchedWizard{}}

	wiz, err := reg.Get("test-fake-mismatched")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	result, err := wiz.Commit(nil)
	if err == nil {
		t.Fatalf("Commit = %+v, <nil>, want a signals.ErrSignalMismatch error", result)
	}
	if !errors.Is(err, signals.ErrSignalMismatch) {
		t.Fatalf("Commit error = %v, want it to wrap signals.ErrSignalMismatch", err)
	}
	if result.Contents != "" {
		t.Fatalf("Commit returned non-empty contents alongside an error: %q", result.Contents)
	}
}

// fakeUnparseableWizard produces text that is not valid Alloy at all, which
// is what a real wizard emits when an operator's input contains something
// like an unbalanced quote — the case RenderWizard exists to show diagnostics
// for.
type fakeUnparseableWizard struct{}

func (fakeUnparseableWizard) Kind() string               { return "test-fake-unparseable" }
func (fakeUnparseableWizard) Schema() Schema             { return Schema{Kind: "test-fake-unparseable"} }
func (fakeUnparseableWizard) Role(map[string]any) string { return "metrics" }
func (fakeUnparseableWizard) Commit(map[string]any) (CommitResult, error) {
	// An unterminated string: the shape a quote in operator input produces.
	return CommitResult{Contents: `prometheus.scrape "app" { job_name = "oops`}, nil
}

// Role enforcement must not swallow a syntax error and report it as a role
// mismatch. RenderWizard is a preview surface, and an operator whose input
// produced broken Alloy needs Stage 1's line-level diagnostics — not "does
// not match its declared role", which is both less useful and untrue, since
// nothing was determined about the role.
//
// This regressed once: wrapping every wizard in roleEnforced turned the
// preview RPC for invalid syntax from 200-with-diagnostics into a 400, caught
// by internal/mgmtapi's "surfaces stage-1 syntax diagnostics from
// RenderWizard without failing the RPC" spec.
//
// Red run, executed: deleting the errors.Is(checkErr, signals.ErrParse)
// branch in roleEnforced.Commit fails this test with `Commit returned an
// error for unparseable contents` (and also re-fails the mgmtapi spec above).
func TestRoleEnforcedPassesThroughUnparseableContents(t *testing.T) {
	reg := &Registry{wizards: make(map[string]Wizard)}
	reg.wizards[fakeUnparseableWizard{}.Kind()] = roleEnforced{Wizard: fakeUnparseableWizard{}}

	wiz, err := reg.Get("test-fake-unparseable")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	result, err := wiz.Commit(nil)
	if err != nil {
		t.Fatalf("Commit returned an error for unparseable contents (%v) — the syntax error belongs "+
			"to Stage 1, which reports it with actionable diagnostics; reporting it here replaces "+
			"those diagnostics with a role claim that was never evaluated", err)
	}
	if result.Contents == "" {
		t.Fatalf("Commit discarded the contents, so nothing is left for Stage 1 to diagnose")
	}
	if result.Role != "metrics" {
		t.Fatalf("Role = %q, want the declared role to still be carried through", result.Role)
	}
}

// The pass-through above must stay narrow: a pipeline that DOES parse and
// carries a disallowed signal is still refused. Without this, a future edit
// that widened the ErrParse branch into "ignore any checkRole error" would
// disable G6 for wizards and no test would notice.
func TestRoleEnforcedStillRefusesParseableMismatch(t *testing.T) {
	reg := &Registry{wizards: make(map[string]Wizard)}
	reg.wizards[fakeMismatchedWizard{}.Kind()] = roleEnforced{Wizard: fakeMismatchedWizard{}}

	wiz, err := reg.Get("test-fake-mismatched")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := wiz.Commit(nil); !errors.Is(err, signals.ErrSignalMismatch) {
		t.Fatalf("Commit error = %v, want a still-enforced signals.ErrSignalMismatch — the "+
			"unparseable-contents exemption must not have widened into ignoring role mismatches", err)
	}
}
