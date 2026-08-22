package reconcile

import (
	"errors"
	"strings"
	"testing"

	"shepherd/internal/signals"
)

// provenSignals builds a signals.Signals whose Combined is sig and whose
// Proven() is true (no Unknown, no Unclassified) — the ordinary case a
// pipeline with only recognized components produces.
func provenSignals(sig ...signals.Signal) signals.Signals {
	return signals.Signals{Combined: signals.NewSet(sig...)}
}

// unprovenSignals builds a signals.Signals whose Proven() is false because
// it carries an unresolved top-level component — the same shape
// internal/signals.Derive returns for a pipeline with an unrecognized
// block.
func unprovenSignals(sig ...signals.Signal) signals.Signals {
	return signals.Signals{
		Combined: signals.NewSet(sig...),
		Unknown:  []signals.UnknownComponent{{Component: "some.unrecognized_component"}},
	}
}

func TestCompare_UnknownRoleFailsLoud(t *testing.T) {
	_, err := Compare(Declared{Role: "made_up_role"}, nil, Observed{})
	if err == nil {
		t.Fatal("want an error for an unrecognized declared role, got nil")
	}
	if !errors.Is(err, signals.ErrUnknownRole) {
		t.Fatalf("want error wrapping signals.ErrUnknownRole, got: %v", err)
	}
}

func TestCompare_EmptyEverythingIsQuiet(t *testing.T) {
	findings, err := Compare(Declared{Role: "metrics"}, nil, Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want zero findings for a collector with nothing served and nothing observed, got %+v", findings)
	}
}

// TestNoInventoryNeverContradicts pins the single most important
// correctness property named in the W6 brief: a collector Shepherd serves
// real pipelines to, but whose beacon has never reported (or whose entire
// inventory has aged out), must produce ZERO findings — never a
// "served-but-not-observed" contradiction manufactured from missing data.
func TestNoInventoryNeverContradicts(t *testing.T) {
	served := []ServedPipeline{
		{Name: "app-logs", ControllerPath: "pipe_app_logs", Signals: provenSignals(signals.Logs)},
		{Name: "app-metrics", ControllerPath: "pipe_app_metrics", Signals: provenSignals(signals.Metrics)},
	}
	findings, err := Compare(Declared{Role: "singleton"}, served, Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want zero findings when Observed is empty (never-reported or fully-expired), got %+v", findings)
	}
}

func TestCompare_RoleSignalMismatch(t *testing.T) {
	served := []ServedPipeline{
		{Name: "app-metrics", ControllerPath: "pipe_app_metrics", Signals: provenSignals(signals.Metrics)},
	}
	findings, err := Compare(Declared{Role: "logs"}, served, Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly one finding, got %+v", findings)
	}
	f := findings[0]
	if f.Kind != KindRoleSignalMismatch {
		t.Errorf("want KindRoleSignalMismatch, got %v", f.Kind)
	}
	if f.Sources != [2]Source{SourceDeclared, SourceServed} {
		t.Errorf("want Sources {declared, served}, got %v", f.Sources)
	}
	if f.PipelineName != "app-metrics" {
		t.Errorf("want PipelineName app-metrics, got %q", f.PipelineName)
	}
	for _, want := range []string{`role "logs"`, "app-metrics", "metrics"} {
		if !strings.Contains(f.Summary, want) {
			t.Errorf("Summary %q does not mention %q — a finding must name both sides' claims, not just say \"inconsistent\"", f.Summary, want)
		}
	}
}

func TestCompare_UnrestrictedRoleNeverMismatches(t *testing.T) {
	served := []ServedPipeline{
		{Name: "self-monitor", ControllerPath: "pipe_self_monitor", Signals: provenSignals(signals.Metrics, signals.Logs, signals.Traces, signals.Profiles)},
	}
	findings, err := Compare(Declared{Role: "singleton"}, served, Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want zero findings for an Unrestricted role regardless of served signals, got %+v", findings)
	}
}

// TestCompare_UnprovenServedSignalsAreWorstCase mirrors
// internal/merge/enforce.go's own fail-safe stance: a pipeline Derive could
// not fully classify must be checked as if it carries every signal, not
// just the ones it could prove — otherwise an under-counted pipeline could
// slip past a restrictive role's policy undetected.
func TestCompare_UnprovenServedSignalsAreWorstCase(t *testing.T) {
	// Combined is empty (nothing PROVEN), but the pipeline is not Proven()
	// — a metrics-only role must still be flagged, because "empty" here
	// means "unknown", not "nothing".
	served := []ServedPipeline{
		{Name: "mystery", ControllerPath: "pipe_mystery", Signals: unprovenSignals()},
	}
	findings, err := Compare(Declared{Role: "metrics"}, served, Observed{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want one finding for an unproven pipeline served to a restricted role, got %+v", findings)
	}
	if findings[0].Kind != KindRoleSignalMismatch {
		t.Errorf("want KindRoleSignalMismatch, got %v", findings[0].Kind)
	}
}

func TestCompare_UnservedComponentObserved(t *testing.T) {
	served := []ServedPipeline{
		{Name: "app-logs", ControllerPath: "pipe_app_logs", Signals: provenSignals(signals.Logs)},
	}
	observed := Observed{Components: []ObservedComponent{
		{ControllerPath: "pipe_app_logs", Healthy: true},           // matches served — no finding
		{ControllerPath: "prometheus.scrape.local", Healthy: true}, // BYO local config Shepherd never served
	}}
	findings, err := Compare(Declared{Role: "logs"}, served, observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly one finding, got %+v", findings)
	}
	f := findings[0]
	if f.Kind != KindUnservedComponentObserved {
		t.Errorf("want KindUnservedComponentObserved, got %v", f.Kind)
	}
	if f.Sources != [2]Source{SourceServed, SourceObserved} {
		t.Errorf("want Sources {served, observed}, got %v", f.Sources)
	}
	if f.ControllerPath != "prometheus.scrape.local" {
		t.Errorf("want ControllerPath prometheus.scrape.local, got %q", f.ControllerPath)
	}
	for _, want := range []string{`role "logs"`, "prometheus.scrape.local"} {
		if !strings.Contains(f.Summary, want) {
			t.Errorf("Summary %q does not mention %q", f.Summary, want)
		}
	}
}

// TestCompare_StaleObservationStillCountsButIsFlagged pins design note 4:
// a partially-expired (but still-present, not-yet-swept) observation is
// still POSITIVE evidence — it must still produce a finding, just one that
// visibly says the evidence is stale, never silently dropped and never
// upgraded into a stronger claim than "it was seen".
func TestCompare_StaleObservationStillCountsButIsFlagged(t *testing.T) {
	observed := Observed{Components: []ObservedComponent{
		{ControllerPath: "prometheus.scrape.local", Healthy: true, Stale: true},
	}}
	findings, err := Compare(Declared{Role: "logs"}, nil, observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want one finding even though the observation is stale, got %+v", findings)
	}
	if !findings[0].Stale {
		t.Error("want Finding.Stale == true")
	}
	if !strings.Contains(findings[0].Summary, "stale") {
		t.Errorf("Summary %q does not flag the evidence as stale", findings[0].Summary)
	}
}

func TestCompare_DuplicateObservedIdentityDeduplicates(t *testing.T) {
	observed := Observed{Components: []ObservedComponent{
		{ControllerPath: "prometheus.scrape.local", Healthy: true},
		{ControllerPath: "prometheus.scrape.local", Healthy: false},
	}}
	findings, err := Compare(Declared{Role: "logs"}, nil, observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly one finding for a repeated identity, got %d: %+v", len(findings), findings)
	}
}

func TestCompare_ServedMatchingObservedIsQuiet(t *testing.T) {
	served := []ServedPipeline{
		{Name: "app-metrics", ControllerPath: "pipe_app_metrics", Signals: provenSignals(signals.Metrics)},
	}
	observed := Observed{Components: []ObservedComponent{
		{ControllerPath: "pipe_app_metrics", Healthy: true},
	}}
	findings, err := Compare(Declared{Role: "metrics"}, served, observed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("want zero findings when served and observed agree and role is respected, got %+v", findings)
	}
}

// TestCompare_TableDriven exercises the interesting combinations in one
// place for quick scanning of what each combination is expected to prove.
func TestCompare_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		declared  Declared
		served    []ServedPipeline
		observed  Observed
		wantKinds []Kind
	}{
		{
			name:     "receiver role allows metrics+logs+traces served together",
			declared: Declared{Role: "receiver"},
			served: []ServedPipeline{
				{Name: "otlp", ControllerPath: "pipe_otlp", Signals: provenSignals(signals.Metrics, signals.Logs, signals.Traces)},
			},
		},
		{
			name:     "receiver role rejects profiles",
			declared: Declared{Role: "receiver"},
			served: []ServedPipeline{
				{Name: "profiling", ControllerPath: "pipe_profiling", Signals: provenSignals(signals.Profiles)},
			},
			wantKinds: []Kind{KindRoleSignalMismatch},
		},
		{
			name:     "logs role, beacon-only inventory (baseline observed, nothing unexpected)",
			declared: Declared{Role: "logs"},
			served: []ServedPipeline{
				{Name: "app-logs", ControllerPath: "pipe_app_logs", Signals: provenSignals(signals.Logs)},
			},
			observed: Observed{Components: []ObservedComponent{
				{ControllerPath: "pipe_app_logs", Healthy: true},
			}},
		},
		{
			name:     "metrics role, mismatch AND unserved component both fire independently",
			declared: Declared{Role: "metrics"},
			served: []ServedPipeline{
				{Name: "leaky", ControllerPath: "pipe_leaky", Signals: provenSignals(signals.Metrics, signals.Logs)},
			},
			observed: Observed{Components: []ObservedComponent{
				{ControllerPath: "loki.write.local", Healthy: true},
			}},
			wantKinds: []Kind{KindRoleSignalMismatch, KindUnservedComponentObserved},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := Compare(tc.declared, tc.served, tc.observed)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(findings) != len(tc.wantKinds) {
				t.Fatalf("want %d finding(s) %v, got %d: %+v", len(tc.wantKinds), tc.wantKinds, len(findings), findings)
			}
			for i, k := range tc.wantKinds {
				if findings[i].Kind != k {
					t.Errorf("finding %d: want Kind %v, got %v", i, k, findings[i].Kind)
				}
			}
		})
	}
}
