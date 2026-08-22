package beacon

import (
	"errors"
	"sort"

	"github.com/prometheus/prometheus/prompb"
)

// ComponentObservation is the ONLY shape Project ever returns, and the only
// shape internal/agentapi's beacon handler is allowed to pass to storage. It
// deliberately has no field capable of carrying a raw sample value — see
// doc.go and TestComponentObservationHasNoNumericField, which checks this
// automatically.
type ComponentObservation struct {
	// ComponentName is the reporting Alloy's controller_path label off
	// alloy_component_controller_running_components — the declare-wrapped
	// pipeline name for anything internal/merge assembled (see
	// merge.Assemble's `declare "pipe_<name>"` wrapping), or the root
	// controller path for un-declared components.
	ComponentName string
	// Healthy is true iff every component reporting under ComponentName is
	// in the "healthy" bucket: at least one healthy component, and zero in
	// any other health_type bucket (unhealthy/exited/unknown).
	Healthy bool
}

// runningComponentsMetric is the one metric name Project ever inspects.
// Verified against the pinned Alloy version's actual source, not from
// memory (docs/gateway-tier-plan.md §8 rule 6): grafana/alloy tag v1.18.1,
// internal/runtime/internal/controller/metrics.go, controllerCollector's
// descriptor — "Total number of running components", labels health_type,
// controller_path, controller_id. Every other series in a beacon write
// (alloy_build_info, evaluation timings, anything else prometheus.scrape
// picked up from Alloy's own /metrics) is skipped without its value ever
// being read — see Project's loop.
const runningComponentsMetric = "alloy_component_controller_running_components"

// healthTypeHealthy is the health_type label value the pinned metric uses
// for "this component is up". Every other health_type value (unhealthy,
// exited, unknown — Alloy's component.HealthType enum) counts against
// Healthy below.
const healthTypeHealthy = "healthy"

// ErrNoInstanceLabel is returned when a write request carries no `instance`
// label on any series recognized by Project. prometheus.scrape stamps this
// label on every series it scrapes (job/instance are its own target
// identity, not something this package or the baseline pipeline sets) —
// its absence means the request did not come from the rendered baseline
// pipeline, or arrived some other way this package does not understand.
// Without it there is no key to store the observations under, so Project
// refuses rather than inventing one.
var ErrNoInstanceLabel = errors.New("beacon: write request has no instance label on any recognized series")

// Project reduces a decoded remote_write request to the identity (the
// `instance` label prometheus.scrape stamps on every series from one
// reporting Alloy process) and health of the components that process ran,
// discarding every sample value once it has been read exactly once to
// decide a health bucket. See doc.go's "structural no-raw-sample guarantee"
// for what that promise rests on.
//
// A series whose __name__ is not runningComponentsMetric is skipped without
// its Samples ever being indexed — Project only ever reads a Sample.Value
// for the one metric it understands.
func Project(wr *prompb.WriteRequest) (instanceLabel string, observations []ComponentObservation, err error) {
	// path -> health_type -> summed count. Alloy's controllerCollector emits
	// one series per (controller_path, health_type) pair currently non-zero;
	// summing (rather than "last wins") is defensive against a batch that
	// happens to carry more than one point for the same series, which
	// remote_write's wire format does not forbid.
	counts := make(map[string]map[string]float64)
	var order []string

	for i := range wr.Timeseries {
		ts := &wr.Timeseries[i]

		var metricName, path, healthType string
		for _, l := range ts.Labels {
			switch l.Name {
			case "__name__":
				metricName = l.Value
			case "controller_path":
				path = l.Value
			case "health_type":
				healthType = l.Value
			case "instance":
				if instanceLabel == "" {
					instanceLabel = l.Value
				}
			}
		}

		if metricName != runningComponentsMetric || path == "" || healthType == "" || len(ts.Samples) == 0 {
			continue
		}

		if _, ok := counts[path]; !ok {
			counts[path] = make(map[string]float64)
			order = append(order, path)
		}
		// Last sample in the series wins for a single point; ties broken by
		// summation across samples is intentionally NOT done here — a gauge
		// re-reported within one batch should reflect its latest value, not
		// an inflated sum. See the map-level comment above for why summing
		// across *series* (not samples) still happens: distinct health_type
		// buckets are additive, distinct timestamps of the same bucket are not.
		counts[path][healthType] = ts.Samples[len(ts.Samples)-1].Value
	}

	if instanceLabel == "" {
		return "", nil, ErrNoInstanceLabel
	}

	sort.Strings(order)
	observations = make([]ComponentObservation, 0, len(order))
	for _, path := range order {
		observations = append(observations, ComponentObservation{
			ComponentName: path,
			Healthy:       isHealthy(counts[path]),
		})
	}
	return instanceLabel, observations, nil
}

// isHealthy reports whether byHealthType — one controller_path's
// health_type -> count map — describes an all-healthy component group: at
// least one component counted as healthy, and zero counted in any other
// bucket.
func isHealthy(byHealthType map[string]float64) bool {
	if byHealthType[healthTypeHealthy] <= 0 {
		return false
	}
	for healthType, count := range byHealthType {
		if healthType == healthTypeHealthy {
			continue
		}
		if count > 0 {
			return false
		}
	}
	return true
}
