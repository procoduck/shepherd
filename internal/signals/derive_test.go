package signals

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDerive_Corpus runs Derive against internal/visual's real Alloy corpus
// fixtures (internal/visual/testdata/corpus/*.golden.alloy) — genuine,
// non-trivial pipelines already used elsewhere in the repo as ground truth
// for "what does this Alloy text mean", rather than fixtures invented just
// for this package.
func TestDerive_Corpus(t *testing.T) {
	reg := testRegistry(t)

	cases := []struct {
		file string
		want []Signal
	}{
		// Pure Prometheus scrape chain: discovery.kubernetes and
		// prometheus.remote_write's "targets"-only ports contribute
		// nothing; prometheus.scrape/remote_write's prom.metrics ports
		// contribute Metrics.
		{"minimal-scrape", []Signal{Metrics}},
		// Pure Loki chain: local.file_match is targets-only plumbing;
		// loki.source.file/process/write all carry loki.logs.
		{"logs-chain", []Signal{Logs}},
		// otelcol.receiver.otlp / processor.batch / exporter.otlp: OTLP
		// carries all three signals natively (see wiretype.go's otel.any
		// rationale for why Profiles is not expected here).
		{"otel-three-signals", []Signal{Metrics, Logs, Traces}},
		// Fan-in discovery graph feeding one prometheus.scrape: still
		// metrics-only, discovery.relabel is targets-only plumbing twice
		// over.
		{"fanin-fanout", []Signal{Metrics}},
		// Mixed pipeline: a Prometheus branch and a Loki branch sharing one
		// discovery.kubernetes source.
		{"kitchen-sink", []Signal{Metrics, Logs}},
		{"nested-blocks", []Signal{Metrics}},
		// "disabled" is only a generator comment; the block is still
		// present in the text and Derive has no notion of disabled nodes,
		// so prometheus.remote_write still contributes Metrics.
		{"disabled-node", []Signal{Metrics}},
		{"label-edgecases", []Signal{Metrics}},
		// remote.kubernetes.secret has no inputs/outputs ports at all
		// (attribute-only component) and contributes nothing.
		{"bindings-secret", []Signal{Metrics}},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "visual", "testdata", "corpus", tc.file+".golden.alloy")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", path, err)
			}

			got, err := Derive(string(data), reg)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if got.HasUnknown() {
				t.Fatalf("unexpected unknown components: %+v", got.Unknown)
			}
			if len(got.Unclassified) != 0 {
				t.Fatalf("unexpected unclassified wire types: %v", got.Unclassified)
			}
			want := NewSet(tc.want...)
			if !got.Combined.Equal(want) {
				t.Fatalf("Combined = %s, want %s\ncontributions: %+v", got.Combined, want, got.Contributions)
			}
		})
	}
}

// TestDerive_TargetsOnlyContributesNoSignal covers a pipeline made entirely
// of discovery components: "targets" is plumbing, not a signal, so a graph
// that never reaches a scraper/tailer/receiver proves an empty, but fully
// PROVEN, signal set.
func TestDerive_TargetsOnlyContributesNoSignal(t *testing.T) {
	reg := testRegistry(t)
	const src = `
discovery.kubernetes "k8s" {
  role = "pod"
}

discovery.relabel "filtered" {
  targets = discovery.kubernetes.k8s.targets
  rule {
    action        = "keep"
    source_labels = ["__meta_kubernetes_pod_phase"]
    regex         = "Running"
  }
}
`
	got, err := Derive(src, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !got.Combined.Empty() {
		t.Fatalf("Combined = %s, want empty", got.Combined)
	}
	if !got.Proven() {
		t.Fatalf("expected a fully proven result, got Unknown=%+v Unclassified=%v", got.Unknown, got.Unclassified)
	}
	if len(got.Contributions) != 2 {
		t.Fatalf("Contributions = %+v, want one entry per component (2)", got.Contributions)
	}
	for _, c := range got.Contributions {
		if !c.Signals.Empty() {
			t.Fatalf("component %s/%s contributed %s, want empty (targets-only)", c.Component, c.Label, c.Signals)
		}
	}
}

// TestDerive_MultiSignalSingleComponent covers one component contributing
// more than one signal at once (an OTLP receiver producing metrics, logs
// and traces on separate ports).
func TestDerive_MultiSignalSingleComponent(t *testing.T) {
	reg := testRegistry(t)
	const src = `
otelcol.receiver.otlp "ingest" {
  grpc {
    endpoint = "0.0.0.0:4317"
  }
  output {
    metrics = [otelcol.exporter.otlp.remote.input]
    logs    = [otelcol.exporter.otlp.remote.input]
    traces  = [otelcol.exporter.otlp.remote.input]
  }
}

otelcol.exporter.otlp "remote" {
  client {
    endpoint = "otel-gateway.example.com:4317"
  }
}
`
	got, err := Derive(src, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	want := NewSet(Metrics, Logs, Traces)
	if !got.Combined.Equal(want) {
		t.Fatalf("Combined = %s, want %s", got.Combined, want)
	}
	var ingest Contribution
	found := false
	for _, c := range got.Contributions {
		if c.Component == "otelcol.receiver.otlp" && c.Label == "ingest" {
			ingest, found = c, true
		}
	}
	if !found {
		t.Fatalf("no contribution recorded for otelcol.receiver.otlp/ingest; Contributions=%+v", got.Contributions)
	}
	if !ingest.Signals.Equal(want) {
		t.Fatalf("otelcol.receiver.otlp alone contributed %s, want %s", ingest.Signals, want)
	}
}

// TestDerive_UnknownComponent is the honesty requirement: a component not in
// the schema (typo, or a component from an Alloy release the pinned schema
// predates) must be reported, never silently dropped, and must not corrupt
// what WAS proven about the rest of the pipeline.
func TestDerive_UnknownComponent(t *testing.T) {
	reg := testRegistry(t)
	const src = `
totally.bogus.component "x" {
  foo = "bar"
}

prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`
	got, err := Derive(src, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if !got.HasUnknown() {
		t.Fatalf("expected HasUnknown() == true, got Unknown=%+v", got.Unknown)
	}
	if len(got.Unknown) != 1 || got.Unknown[0].Component != "totally.bogus.component" || got.Unknown[0].Label != "x" {
		t.Fatalf("Unknown = %+v, want exactly one entry {totally.bogus.component, x}", got.Unknown)
	}
	if got.Proven() {
		t.Fatalf("Proven() should be false when an unknown component is present")
	}
	// The known components in the same file still contribute what they
	// prove — an unknown component elsewhere must not erase that.
	if !got.Combined.Has(Metrics) {
		t.Fatalf("expected Metrics still derived from the known components, got %s", got.Combined)
	}
}

// TestDerive_ParseError asserts a syntactically invalid config is an error,
// never an empty (and therefore falsely "proven safe") Signals value.
func TestDerive_ParseError(t *testing.T) {
	reg := testRegistry(t)
	got, err := Derive(`prometheus.scrape "app" {`, reg)
	if err == nil {
		t.Fatalf("Derive returned nil error for unterminated block; got %+v", got)
	}
}

func TestDerive_NilRegistry(t *testing.T) {
	_, err := Derive(`logging {}`, nil)
	if err == nil {
		t.Fatalf("expected an error for a nil *schema.Registry")
	}
}

// TestDerive_NonComponentBlocksSkipped ensures a legitimate Alloy syntax
// construct that is not a component (logging{}, and friends) is skipped
// quietly rather than reported as an unknown component — the two must not be
// conflated, or every ordinary Alloy file with a logging{} block would flag
// a false unknown-component warning.
func TestDerive_NonComponentBlocksSkipped(t *testing.T) {
	reg := testRegistry(t)
	const src = `
logging {
  level = "info"
}

prometheus.scrape "app" {
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://example.com/write"
  }
}
`
	got, err := Derive(src, reg)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got.HasUnknown() {
		t.Fatalf("logging{} should not be reported as an unknown component, got Unknown=%+v", got.Unknown)
	}
	if !got.Combined.Equal(NewSet(Metrics)) {
		t.Fatalf("Combined = %s, want %s", got.Combined, NewSet(Metrics))
	}
}

// TestDerive_EmptyConfig covers the degenerate case: a config with nothing in
// it is a valid, fully-proven, empty pipeline — not an error.
func TestDerive_EmptyConfig(t *testing.T) {
	reg := testRegistry(t)
	got, err := Derive("", reg)
	if err != nil {
		t.Fatalf("Derive(\"\"): %v", err)
	}
	if !got.Combined.Empty() || !got.Proven() {
		t.Fatalf("Derive(\"\") = %+v, want an empty, proven result", got)
	}
}

// TestDerive_RecursesIntoContainerBlocks pins the fix for a real defect found
// by review: Derive skipped `foreach` and `declare` wholesale, so a pipeline
// whose components all live inside one derived an EMPTY signal set that still
// reported Proven() == true. Enforce then passed trivially for every role —
// the guard silently covering nothing, which is the failure class
// docs/project-status.md §6 names. Revert the containerBlocks recursion in
// derive.go and this test goes red.
func TestDerive_RecursesIntoContainerBlocks(t *testing.T) {
	reg := testRegistry(t)
	for _, tc := range []struct {
		name string
		src  string
	}{
		{
			name: "foreach",
			src: `
foreach "each" {
  collection = [1, 2]
  var        = "n"

  prometheus.scrape "inner" {
    targets    = []
    forward_to = []
  }
}
`,
		},
		{
			name: "declare",
			src: `
declare "inner_pipeline" {
  prometheus.scrape "inner" {
    targets    = []
    forward_to = []
  }
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Derive(tc.src, reg)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if !got.Combined.Has(Metrics) {
				t.Fatalf("Combined = %s, want Metrics — a component nested in %s must count "+
					"exactly as much as a top-level one, or role enforcement is a no-op for it",
					got.Combined, tc.name)
			}
			if len(got.Contributions) != 1 {
				t.Fatalf("Contributions = %+v, want the nested component recorded", got.Contributions)
			}
		})
	}
}
