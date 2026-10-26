package signals

import (
	"encoding/json"
	"io/fs"
	"sort"
	"testing"
	"testing/fstest"

	"shepherd/internal/schema"
	"shepherd/internal/version"
)

// pinnedOverrides is every curated signal override the schema overlay
// carries (a component's "signals" key). Each narrows a component whose
// generic otel.any input, or generic metrics/logs/traces output block,
// overstates what it carries, and each was checked against the Alloy v1.21.2
// reference docs (and, for the four the k8s-monitoring chart renders, the
// component source). Adding, changing or removing one changes what restricted
// roles accept, so this list must change with it, on purpose.
var pinnedOverrides = map[string]Set{
	// "accepts OTLP-formatted metrics … Other telemetry signals are ignored."
	"otelcol.exporter.prometheus": NewSet(Metrics),
	// "accepts OTLP-formatted logs … Other telemetry signals are ignored."
	"otelcol.exporter.loki": NewSet(Logs),
	// "accepts logs … Other telemetry signals are ignored."
	"otelcol.exporter.syslog": NewSet(Logs),
	// Receivers whose generic output block lists all three signals but
	// which only ever emit one.
	"otelcol.receiver.prometheus":             NewSet(Metrics),
	"otelcol.receiver.influxdb":               NewSet(Metrics),
	"otelcol.receiver.nginx":                  NewSet(Metrics),
	"otelcol.receiver.vcenter":                NewSet(Metrics),
	"otelcol.receiver.file_stats":             NewSet(Metrics),
	"otelcol.receiver.awsecscontainermetrics": NewSet(Metrics),
	"otelcol.receiver.loki":                   NewSet(Logs),
	"otelcol.receiver.awscloudwatch":          NewSet(Logs),
	"otelcol.receiver.cloudflare":             NewSet(Logs),
	"otelcol.receiver.filelog":                NewSet(Logs),
	"otelcol.receiver.syslog":                 NewSet(Logs),
	"otelcol.receiver.tcplog":                 NewSet(Logs),
	"otelcol.receiver.jaeger":                 NewSet(Traces),
	"otelcol.receiver.zipkin":                 NewSet(Traces),
	"otelcol.receiver.solace":                 NewSet(Traces),
	// Processors whose `input` the docs say accepts only one signal.
	"otelcol.processor.cumulativetodelta": NewSet(Metrics),
	"otelcol.processor.deltatocumulative": NewSet(Metrics),
	"otelcol.processor.interval":          NewSet(Metrics),
	"otelcol.processor.metric_start_time": NewSet(Metrics),
	"otelcol.processor.span":              NewSet(Traces),
	"otelcol.processor.discovery":         NewSet(Traces),
}

// TestSignalOverrides_Pinned: the overlay carries exactly pinnedOverrides, and
// each overridden component, alone in a pipeline, derives exactly its
// override (proven).
func TestSignalOverrides_Pinned(t *testing.T) {
	reg := testRegistry(t)
	defs, err := loadComponentDefs(reg)
	if err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for name, def := range defs {
		if def.Override != nil {
			got = append(got, name)
		}
	}
	for name := range pinnedOverrides {
		want = append(want, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("overridden components = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("overridden components = %v, want %v", got, want)
		}
	}

	for name, wantSet := range pinnedOverrides {
		sig, err := Derive(name+` "x" { }`, reg)
		if err != nil {
			t.Fatalf("%s: Derive: %v", name, err)
		}
		if !sig.Proven() || !sig.Combined.Equal(wantSet) {
			t.Errorf("%s: Combined = %s (proven %v), want %s", name, sig.Combined, sig.Proven(), wantSet)
		}
	}
}

// TestSignalOverrides_OnlyNarrow: every override is a non-empty subset of
// what the component's wire types classify as. An override is a correction
// of an overstated generic port, never a way to add or remove a signal the
// ports actually carry.
func TestSignalOverrides_OnlyNarrow(t *testing.T) {
	defs, err := loadComponentDefs(testRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, def := range defs {
		if def.Override == nil {
			continue
		}
		wire, unclassified := wireSignals(def.WireTypes)
		if len(unclassified) != 0 {
			t.Errorf("%s: unclassified wire types %v", name, unclassified)
		}
		if def.Override.Empty() || !def.Override.subsetOf(wire) {
			t.Errorf("%s: override %s is not a non-empty subset of its wire signals %s", name, def.Override, wire)
		}
	}
}

// TestSignalOverrides_FailWide: an override Derive cannot trust is ignored,
// so the component counts with its full wire-type signals. A malformed value,
// an empty list, an unknown signal name, and one that names a signal the
// ports do not carry (an attempt to widen or swap) all fall back.
func TestSignalOverrides_FailWide(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override any
	}{
		{"not a list", "metrics"},
		{"empty list", []any{}},
		{"unknown name", []any{"metrics", "bogus"}},
		{"not a string", []any{1}},
		{"outside the wire signals", []any{"profiles"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := overriddenRegistry(t, "otelcol.receiver.prometheus", tc.override)
			sig, err := Derive(`otelcol.receiver.prometheus "x" { }`, reg)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if want := NewSet(Metrics, Logs, Traces); !sig.Combined.Equal(want) {
				t.Fatalf("Combined = %s, want the wire-type set %s", sig.Combined, want)
			}
		})
	}
}

// overriddenRegistry is the pinned registry with component's overlay
// "signals" set to override.
func overriddenRegistry(t *testing.T, component string, override any) *schema.Registry {
	t.Helper()
	artifactPath := "artifacts/" + version.AlloySchemaVersion + ".json"
	artifact, err := fs.ReadFile(schema.Embedded, artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(schema.Embedded, "artifacts/overlay.json")
	if err != nil {
		t.Fatal(err)
	}
	var overlay map[string]any
	if err := json.Unmarshal(raw, &overlay); err != nil {
		t.Fatal(err)
	}
	comps := overlay["components"].(map[string]any) //nolint:errcheck,forcetypeassert // the shipped overlay is an object
	comp, _ := comps[component].(map[string]any)    //nolint:errcheck // absent is fine: a fresh entry
	if comp == nil {
		comp = map[string]any{}
	}
	comp["signals"] = override
	comps[component] = comp
	doctored, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := schema.New(fstest.MapFS{
		artifactPath:             {Data: artifact},
		"artifacts/overlay.json": {Data: doctored},
	}, version.AlloySchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
