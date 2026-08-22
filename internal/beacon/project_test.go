package beacon

import (
	"errors"
	"reflect"
	"testing"

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

func TestProject_HealthyComponent(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "pipe_minimal_scrape"),
			lbl("health_type", "healthy"),
			lbl("instance", "10.0.0.1:12345"),
		}, 3),
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "pipe_minimal_scrape"),
			lbl("health_type", "unhealthy"),
			lbl("instance", "10.0.0.1:12345"),
		}, 0),
	}}

	instance, obs, err := Project(wr)
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
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "pipe_logs_chain"),
			lbl("health_type", "healthy"),
			lbl("instance", "10.0.0.2:12345"),
		}, 1),
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "pipe_logs_chain"),
			lbl("health_type", "unhealthy"),
			lbl("instance", "10.0.0.2:12345"),
		}, 1),
	}}

	_, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(obs) != 1 || obs[0].Healthy {
		t.Fatalf("obs = %+v, want one unhealthy pipe_logs_chain", obs)
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
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric),
			lbl("controller_path", "pipe_kitchen_sink"),
			lbl("health_type", "healthy"),
			lbl("instance", "10.0.0.3:12345"),
		}, 2),
	}}

	instance, obs, err := Project(wr)
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
			lbl("controller_path", "pipe_x"),
			lbl("health_type", "healthy"),
		}, 1),
	}}

	_, _, err := Project(wr)
	if !errors.Is(err, ErrNoInstanceLabel) {
		t.Fatalf("err = %v, want ErrNoInstanceLabel", err)
	}
}

func TestProject_DeterministicOrder(t *testing.T) {
	wr := &prompb.WriteRequest{Timeseries: []prompb.TimeSeries{
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric), lbl("controller_path", "pipe_zzz"),
			lbl("health_type", "healthy"), lbl("instance", "i"),
		}, 1),
		series([]prompb.Label{
			lbl("__name__", runningComponentsMetric), lbl("controller_path", "pipe_aaa"),
			lbl("health_type", "healthy"), lbl("instance", "i"),
		}, 1),
	}}
	_, obs, err := Project(wr)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(obs) != 2 || obs[0].ComponentName != "pipe_aaa" || obs[1].ComponentName != "pipe_zzz" {
		t.Fatalf("obs = %+v, want sorted [pipe_aaa, pipe_zzz]", obs)
	}
}
