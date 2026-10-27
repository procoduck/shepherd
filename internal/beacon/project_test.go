package beacon

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/prometheus/prometheus/model/value"
	"github.com/prometheus/prometheus/prompb"
)

// TestComponentObservationHasNoNumericField is the automated half of the
// structural no-raw-sample guarantee (doc.go): it fails the moment
// ComponentObservation grows a field capable of carrying a raw sample
// value, so that guarantee is checked, not merely asserted in a comment.
//
// Red run: add `Value float64` to ComponentObservation in project.go. This
// test then fails with "ComponentObservation has a numeric field Value —
// raw sample values must never be representable here", which is exactly
// the failure it exists to catch.
func TestComponentObservationHasNoNumericField(t *testing.T) {
	typ := reflect.TypeOf(ComponentObservation{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() { //nolint:exhaustive // deliberately allow-listing every non-numeric Kind by omission; any Kind not named below is treated as "not numeric", which is correct for every Kind reflect defines other than the ones this switch names
		case reflect.Float32, reflect.Float64,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			t.Fatalf("ComponentObservation has a numeric field %s (%s) — raw sample values must never be representable here",
				f.Name, f.Type.Kind())
		}
	}
	// Also pin the exact field set, so a numeric field renamed to dodge the
	// Kind check above (there is no such Kind, but pin the shape anyway)
	// still shows up as an unexpected change here.
	if typ.NumField() != 2 {
		t.Fatalf("ComponentObservation has %d fields, expected exactly 2 (ComponentName, Healthy) — "+
			"a new field changes what this type can carry and needs deliberate review", typ.NumField())
	}
}

func lbl(name, value string) prompb.Label { return prompb.Label{Name: name, Value: value} }

func series(labels []prompb.Label, value float64) prompb.TimeSeries {
	return prompb.TimeSeries{
		Labels:  labels,
		Samples: []prompb.Sample{{Value: value, Timestamp: 1000}},
	}
}

// ctrlSeries builds one alloy_component_controller_running_components series
// in the label shape the pinned Alloy really emits (v1.20.1, identical in
// v1.18.1): internal/runtime/internal/controller/metrics.go's
// newControllerCollector stamps two CONSTANT labels per loader —
// controller_path (the parent's path, always with a leading "/", from
// loader.go's splitPath) and controller_id (the loader's own id) — plus the
// health_type variable label. Under remotecfg, Shepherd's declare-wrapped
// served config (`declare "pipe_x" {...}` + `pipe_x "default" { }`) reports:
//
//	remotecfg root loader:      controller_path="/"                         controller_id="remotecfg"
//	inside pipe_x:              controller_path="/remotecfg"                controller_id="pipe_x.default"
//	module feat "y" in pipe_x:  controller_path="/remotecfg/pipe_x.default" controller_id="feat.y"
//	local bootstrap config:     controller_path="/"                         controller_id=""
//
// Earlier fixtures invented controller_path="pipe_x", a shape Alloy never
// emits — which is how Project shipped reading only controller_path, and
// every pipeline collapsed onto "/remotecfg".
func ctrlSeries(instance, controllerPath, controllerID, healthType string, value float64) prompb.TimeSeries {
	return series([]prompb.Label{
		lbl("__name__", runningComponentsMetric),
		lbl("controller_id", controllerID),
		lbl("controller_path", controllerPath),
		lbl("health_type", healthType),
		lbl("instance", instance),
	}, value)
}

// pipeSeries is a series from the loader directly inside pipeline pipe.
func pipeSeries(instance, pipe, healthType string, value float64) prompb.TimeSeries {
	return ctrlSeries(instance, "/remotecfg", pipe+".default", healthType, value)
}

func TestProject_HealthyComponent(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("10.0.0.1:12345", "pipe_minimal_scrape", "healthy", 3),
		pipeSeries("10.0.0.1:12345", "pipe_minimal_scrape", "unhealthy", 0),
	}}

	instance, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if instance != "10.0.0.1:12345" {
		t.Fatalf("instance = %q, want 10.0.0.1:12345", instance)
	}
	if len(obs) != 1 || obs[0].ComponentName != "pipe_minimal_scrape" || !obs[0].Healthy {
		t.Fatalf("obs = %+v, want one healthy pipe_minimal_scrape", obs)
	}
}

func TestProject_UnhealthyComponent(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("10.0.0.2:12345", "pipe_logs_chain", "healthy", 1),
		pipeSeries("10.0.0.2:12345", "pipe_logs_chain", "unhealthy", 1),
	}}

	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(obs) != 1 || obs[0].ComponentName != "pipe_logs_chain" || obs[0].Healthy {
		t.Fatalf("obs = %+v, want one unhealthy pipe_logs_chain", obs)
	}
}

// TestProject_TwoPipelinesReportedSeparately is the #378 regression: every
// declare-wrapped pipeline's loader shares controller_path="/remotecfg", so a
// projection keyed on controller_path alone collapsed them all onto one
// "/remotecfg" row (last write wins) and reconciliation never saw a pipe_*
// component at all.
func TestProject_TwoPipelinesReportedSeparately(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("i", "pipe_app_logs", "healthy", 2),
		pipeSeries("i", "pipe_app_metrics", "healthy", 1),
		pipeSeries("i", "pipe_app_metrics", "unhealthy", 1),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := []ComponentObservation{
		{ComponentName: "pipe_app_logs", Healthy: true},
		{ComponentName: "pipe_app_metrics", Healthy: false},
	}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("obs = %+v, want %+v", obs, want)
	}
}

// TestProject_NestedModuleRolledUp: a module instantiated inside a pipeline
// runs its own loader one level deeper; its components belong to the
// pipeline, so it reports under the pipeline's key, not its own.
func TestProject_NestedModuleRolledUp(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("i", "pipe_k8s", "healthy", 1),
		ctrlSeries("i", "/remotecfg/pipe_k8s.default", "feat.pods", "healthy", 4),
		ctrlSeries("i", "/remotecfg/pipe_k8s.default/feat.pods", "inner.x", "healthy", 2),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := []ComponentObservation{{ComponentName: "pipe_k8s", Healthy: true}}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("obs = %+v, want %+v", obs, want)
	}
}

// TestProject_UnhealthyNestedControllerMakesPipelineUnhealthy: health
// aggregates across every controller under a pipeline — one non-healthy
// component in a nested module is enough, even when the pipeline's own
// loader is all healthy, and regardless of series order.
func TestProject_UnhealthyNestedControllerMakesPipelineUnhealthy(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		ctrlSeries("i", "/remotecfg/pipe_k8s.default", "feat.pods", "exited", 1),
		ctrlSeries("i", "/remotecfg/pipe_k8s.default", "feat.pods", "healthy", 4),
		pipeSeries("i", "pipe_k8s", "healthy", 3),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := []ComponentObservation{{ComponentName: "pipe_k8s", Healthy: false}}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("obs = %+v, want %+v", obs, want)
	}
}

// TestProject_RootAndBootstrapDropped: the remotecfg root loader (where the
// baseline itself and the pipe_x.default nodes run) and the local bootstrap
// config are not pipelines and are not projected; their `instance` label
// still identifies the write.
func TestProject_RootAndBootstrapDropped(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		ctrlSeries("10.0.0.7:12345", "/", "", "healthy", 1),
		ctrlSeries("10.0.0.7:12345", "/", "remotecfg", "healthy", 5),
		ctrlSeries("10.0.0.7:12345", "/", "remotecfg", "unhealthy", 1),
	}}
	instance, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if instance != "10.0.0.7:12345" {
		t.Fatalf("instance = %q", instance)
	}
	if len(obs) != 0 {
		t.Fatalf("obs = %+v, want none (root/bootstrap are not pipelines)", obs)
	}
}

func TestPipelineKey(t *testing.T) {
	cases := []struct{ path, id, want string }{
		{"/remotecfg", "pipe_x.default", "pipe_x"},
		{"/remotecfg/pipe_x.default", "feat.y", "pipe_x"},
		{"/remotecfg/pipe_x.default/feat.y", "pipe_z.default", "pipe_x"},
		{"/remotecfg", "pipe_9lives.default", "pipe_9lives"},
		// Scraped verbatim from a real grafana/alloy:v1.20.1 running the same
		// declare-wrapped shape from a LOCAL config (no remotecfg prefix):
		{"/", "pipe_app_logs.default", "pipe_app_logs"},
		{"/pipe_k8s.default", "feat.pods", "pipe_k8s"},
		{"/", "remotecfg", ""},
		{"/", "", ""},
		{"/", "mod.local", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := pipelineKey(c.path, c.id); got != c.want {
			t.Errorf("pipelineKey(%q, %q) = %q, want %q", c.path, c.id, got, c.want)
		}
	}
}

// TestProject_IgnoresEverythingElse is the red-run-provable half of the
// relabel/keep design: even when a batch carries unrelated series (as a
// buggy or unfiltered pipeline would), Project must not surface them as
// components, and must not error trying to interpret their values.
func TestProject_IgnoresEverythingElse(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{
			lbl("__name__", "alloy_build_info"),
			lbl("instance", "10.0.0.3:12345"),
		}, 1),
		pipeSeries("10.0.0.3:12345", "pipe_kitchen_sink", "healthy", 2),
	}}

	instance, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if instance != "10.0.0.3:12345" {
		t.Fatalf("instance = %q", instance)
	}
	if len(obs) != 1 || obs[0].ComponentName != "pipe_kitchen_sink" {
		t.Fatalf("obs = %+v, want only pipe_kitchen_sink (alloy_build_info must be ignored)", obs)
	}
}

func TestProject_NoInstanceLabel(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "/remotecfg"),
			lbl("controller_id", "pipe_x.default"),
			lbl("health_type", "healthy"),
		}, 1),
	}}

	_, _, _, err := Project(wr)
	if !errors.Is(err, ErrNoInstanceLabel) {
		t.Fatalf("err = %v, want ErrNoInstanceLabel", err)
	}
}

func TestProject_DeterministicOrder(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("i", "pipe_zzz", "healthy", 1),
		pipeSeries("i", "pipe_aaa", "healthy", 1),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(obs) != 2 || obs[0].ComponentName != "pipe_aaa" || obs[1].ComponentName != "pipe_zzz" {
		t.Fatalf("obs = %+v, want sorted [pipe_aaa, pipe_zzz]", obs)
	}
}

func TestProject_ExtractsCollectorID(t *testing.T) {
	ts := pipeSeries("10.0.0.1:12345", "pipe_x", "healthy", 1)
	ts.Labels = append(ts.Labels, lbl(CollectorIDLabel, "col-abc-123"))
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{ts}}
	instance, collectorID, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if collectorID != "col-abc-123" {
		t.Fatalf("collectorID = %q, want col-abc-123", collectorID)
	}
	if instance != "10.0.0.1:12345" {
		t.Fatalf("instance = %q, want 10.0.0.1:12345", instance)
	}
	if len(obs) != 1 {
		t.Fatalf("observations = %d, want 1", len(obs))
	}
}

func TestProject_MixedCollectorIDsRefused(t *testing.T) {
	// #389: one batch comes from one Alloy; series naming two different
	// collectors cannot be attributed to either, so Project refuses the batch.
	a := pipeSeries("10.0.0.1:12345", "pipe_x", "healthy", 1)
	a.Labels = append(a.Labels, lbl(CollectorIDLabel, "col-a"))
	b := pipeSeries("10.0.0.1:12345", "pipe_y", "healthy", 1)
	b.Labels = append(b.Labels, lbl(CollectorIDLabel, "col-b"))
	same := pipeSeries("10.0.0.1:12345", "pipe_z", "healthy", 1)
	same.Labels = append(same.Labels, lbl(CollectorIDLabel, "col-a"))

	if _, _, _, err := Project(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{a, same}}); err != nil {
		t.Fatalf("one collector id repeated: Project: %v", err)
	}
	_, _, _, err := Project(&prompb.WriteRequest{Timeseries: []prompb.TimeSeries{a, same, b}})
	if !errors.Is(err, ErrMixedCollectorIDs) {
		t.Fatalf("Project err = %v, want ErrMixedCollectorIDs", err)
	}
}

func TestProject_NoCollectorIDLabelIsEmpty(t *testing.T) {
	// A pre-#110 baseline write carries no shepherd_collector_id; Project must
	// still succeed and just report an empty collector id.
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("10.0.0.1:12345", "pipe_x", "healthy", 1),
	}}
	_, collectorID, _, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if collectorID != "" {
		t.Fatalf("collectorID = %q, want empty", collectorID)
	}
}

// staleSeries is a series whose latest sample is Prometheus's staleness
// marker: what remote_write sends for a series the scrape no longer
// produced (a pipeline Alloy unloaded), once, in the next scrape.
func staleSeries(instance, pipe, healthType string) prompb.TimeSeries {
	s := pipeSeries(instance, pipe, healthType, 0)
	s.Samples = []prompb.Sample{{Value: 1, Timestamp: 1000}, {Value: math.Float64frombits(value.StaleNaN), Timestamp: 2000}}
	return s
}

// TestProject_StaleMarkerIsNotAnObservation is the 0.21 walkthrough's
// one-extra-cycle "running but not served": after a disabled pipeline was
// unloaded, the next beacon write carried a staleness marker for its
// series, Project took that NaN as the bucket's value, and NaN passed
// isHealthy — so the row was refreshed, healthy, one scrape after Alloy
// stopped running it. A stale marker ends the series (Prometheus drops it
// from every lookback from then on); it is not a sample.
func TestProject_StaleMarkerIsNotAnObservation(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("i", "pipe_kept", "healthy", 1),
		staleSeries("i", "pipe_gone", "healthy"),
		ctrlSeries("i", "/remotecfg/pipe_gone.default", "feat.x", "healthy", math.Float64frombits(value.StaleNaN)),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := []ComponentObservation{{ComponentName: "pipe_kept", Healthy: true}}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("obs = %+v, want only pipe_kept (a stale marker must not refresh or create a row)", obs)
	}
}

// TestProject_StaleBucketDoesNotCount: one bucket of a live pipeline going
// stale (its unhealthy count dropped out) leaves the live buckets to decide.
func TestProject_StaleBucketDoesNotCount(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		pipeSeries("i", "pipe_x", "healthy", 2),
		staleSeries("i", "pipe_x", "unhealthy"),
	}}
	_, _, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	want := []ComponentObservation{{ComponentName: "pipe_x", Healthy: true}}
	if !reflect.DeepEqual(obs, want) {
		t.Fatalf("obs = %+v, want %+v", obs, want)
	}
}

// TestProject_NonFiniteValueIsNeverHealthy: an ordinary NaN or an infinity
// is a sample (the series is live), but no count of healthy components.
func TestProject_NonFiniteValueIsNeverHealthy(t *testing.T) {
	for name, v := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1)} {
		for _, bucket := range []string{"healthy", "unhealthy"} {
			wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
				pipeSeries("i", "pipe_x", "healthy", 1),
				pipeSeries("i", "pipe_x", bucket, v),
			}}
			if bucket == "healthy" {
				wr.Timeseries = wr.Timeseries[1:]
			}
			_, _, obs, err := Project(wr)
			if err != nil {
				t.Fatalf("%s in %s: Project: %v", name, bucket, err)
			}
			want := []ComponentObservation{{ComponentName: "pipe_x", Healthy: false}}
			if !reflect.DeepEqual(obs, want) {
				t.Fatalf("%s in %s: obs = %+v, want %+v", name, bucket, obs, want)
			}
		}
	}
}
