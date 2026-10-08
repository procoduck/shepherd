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

func TestExclusionFindings_NoneWhenNothingExcluded(t *testing.T) {
	if got := ExclusionFindings(Declared{Role: "metrics"}, nil); len(got) != 0 {
		t.Fatalf("want no findings, got %+v", got)
	}
}
