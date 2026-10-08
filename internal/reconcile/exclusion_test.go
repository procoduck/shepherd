package reconcile

import (
	"strings"
	"testing"

	"shepherd/internal/signals"
)

func TestExclusionFindings_NamesPipelineSignalsAndRole(t *testing.T) {
	findings := ExclusionFindings(Declared{Role: "metrics"}, []ExcludedPipeline{
		{Name: "app-logs", Reason: "signals: ...", Disallowed: signals.NewSet(signals.Logs)},
	})
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Kind != KindRoleSignalExcluded {
		t.Errorf("kind = %q, want %q", f.Kind, KindRoleSignalExcluded)
	}
	if f.Sources != [2]Source{SourceDeclared, SourceServed} {
		t.Errorf("sources = %v, want declared, served", f.Sources)
	}
	if f.PipelineName != "app-logs" {
		t.Errorf("pipeline name = %q", f.PipelineName)
	}
	for _, want := range []string{`"app-logs"`, "excluded", "(logs)", "role metrics"} {
		if !strings.Contains(f.Summary, want) {
			t.Errorf("summary %q does not mention %q", f.Summary, want)
		}
	}
}

func TestExclusionFindings_FallsBackToReasonWithoutDisallowed(t *testing.T) {
	findings := ExclusionFindings(Declared{Role: "metrics"}, []ExcludedPipeline{
		{Name: "broken", Reason: "signal derivation failed, excluded fail-safe: parse error"},
	})
	if len(findings) != 1 || !strings.Contains(findings[0].Summary, "signal derivation failed") {
		t.Fatalf("want one finding carrying the merge reason, got %+v", findings)
	}
}

// When the signal set could not be proven, Disallowed is the worst case the
// check assumed. Naming it as "its signals" without the note would claim the
// pipeline was shown to carry all of them.
func TestExclusionFindings_KeepsTheUnprovenNote(t *testing.T) {
	note := "signal set not provable: unknown components [totally.bogus.component], unclassified wire types [] — assumed worst-case"
	findings := ExclusionFindings(Declared{Role: "metrics"}, []ExcludedPipeline{{
		Name:       "mystery",
		Reason:     "signals: ... (" + note + ")",
		Disallowed: signals.NewSet(signals.Logs, signals.Traces, signals.Profiles),
		Unproven:   note,
	}})
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", findings)
	}
	want := `pipeline "mystery" matches this collector but is excluded from its served config: ` +
		"its signals (logs, traces, profiles) are not allowed on role metrics (" + note + ")"
	if findings[0].Summary != want {
		t.Fatalf("summary = %q\nwant      %q", findings[0].Summary, want)
	}
}

func TestExclusionFindings_NoneWhenNothingExcluded(t *testing.T) {
	if got := ExclusionFindings(Declared{Role: "metrics"}, nil); len(got) != 0 {
		t.Fatalf("want no findings, got %+v", got)
	}
}
