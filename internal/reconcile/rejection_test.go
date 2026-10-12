package reconcile

import (
	"strings"
	"testing"
)

// #297: a collector that rejected its served config was invisible to
// reconciliation — the tab said "the served and observed state below cannot
// agree" and listed nothing. The rejection is now a finding, naming the
// pipeline the error's line falls in when it can be attributed.
func TestRejectionFinding(t *testing.T) {
	cases := []struct {
		name      string
		in        Rejection
		pipeline  string
		wantIn    []string
		wantNotIn []string
	}{
		{
			name:     "line in a pipeline's text",
			in:       Rejection{AboutCurrent: true, MergedLine: 40, PipelineName: "app-metrics", PipelineLine: 5},
			pipeline: "app-metrics",
			wantIn:   []string{`rejected in pipeline "app-metrics" (line 5 of its text`, "line 40 of the merged config", "previous config"},
		},
		{
			name:     "line on the declare wrapper",
			in:       Rejection{AboutCurrent: true, MergedLine: 12, PipelineName: "app-metrics"},
			pipeline: "app-metrics",
			wantIn:   []string{`rejected in pipeline "app-metrics"`, "declare wrapper", "line 12 of the merged config"},
		},
		{
			name:      "line outside every block",
			in:        Rejection{AboutCurrent: true, MergedLine: 90},
			wantIn:    []string{"line 90 of the merged config", "outside every pipeline"},
			wantNotIn: []string{"rejected in pipeline"},
		},
		{
			name:      "no line in the error",
			in:        Rejection{AboutCurrent: true},
			wantIn:    []string{"rejected its served config", "names no line"},
			wantNotIn: []string{"rejected in pipeline"},
		},
		{
			name: "an earlier config",
			in:   Rejection{MergedLine: 40, PipelineName: "stale-guess", PipelineLine: 3},
			// Review of #327: Alloy re-reports only on a changed error, so after
			// an unrelated served change the collector is STILL failing — the
			// finding must say so, not that it "has not reported".
			wantIn:    []string{"still failing", "recorded against an earlier served config", "previous config"},
			wantNotIn: []string{"stale-guess", "rejected in pipeline", "has not reported"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := RejectionFinding(tc.in)
			if f.Kind != KindServedConfigRejected {
				t.Errorf("kind = %q, want %q", f.Kind, KindServedConfigRejected)
			}
			if f.Sources != [2]Source{SourceServed, SourceObserved} {
				t.Errorf("sources = %v, want served, observed", f.Sources)
			}
			if f.PipelineName != tc.pipeline {
				t.Errorf("pipeline name = %q, want %q", f.PipelineName, tc.pipeline)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(f.Summary, want) {
					t.Errorf("summary %q does not mention %q", f.Summary, want)
				}
			}
			for _, not := range tc.wantNotIn {
				if strings.Contains(f.Summary, not) {
					t.Errorf("summary %q must not mention %q", f.Summary, not)
				}
			}
		})
	}
}
