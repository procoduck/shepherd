package beacon

import (
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/prometheus/prometheus/prompb"
)

// ComponentObservation is the ONLY shape Project ever returns, and the only
// shape internal/agentapi's beacon handler is allowed to pass to storage. It
// deliberately has no field capable of carrying a raw sample value — see
// doc.go and TestComponentObservationHasNoNumericField, which checks this
// automatically.
type ComponentObservation struct {
	// ComponentName is the managed pipeline the observation is about: the
	// declare label internal/merge wraps it in ("pipe_" + merge.SanitizeName),
	// e.g. "pipe_app_logs". Project derives it from the reporting loader's
	// full controller path (see pipelineKey), rolling every loader nested
	// inside the pipeline (modules, imports, custom components) up to it.
	ComponentName string
	// Healthy is true iff every component running anywhere under the
	// pipeline is in the "healthy" bucket: at least one healthy component,
	// and zero in any other health_type bucket (unhealthy/exited/unknown),
	// summed across all of the pipeline's loaders.
	Healthy bool
}

// runningComponentsMetric is the one metric name Project ever inspects.
// Verified against the pinned Alloy version's actual source, not from
// memory (docs/gateway-tier-plan.md §8 rule 6): grafana/alloy tag v1.18.1,
// re-verified at v1.20.1 for #378, internal/runtime/internal/controller/
// metrics.go, controllerCollector's descriptor — "Total number of running
// components", variable label health_type, CONSTANT labels controller_path
// and controller_id. There is one such collector per loader: controller_path
// is the PARENT loader's path (always with a leading "/", loader.go's
// splitPath) and controller_id is the loader's own id, so neither label
// alone names the loader — see pipelineKey. Every other series in a beacon
// write (alloy_build_info, evaluation timings, anything else
// prometheus.scrape picked up from Alloy's own /metrics) is skipped without
// its value ever being read — see Project's loop.
const runningComponentsMetric = "alloy_component_controller_running_components"

// CollectorIDLabel is the label the rendered baseline pipeline stamps on every
// beacon series (BaselineConfig.CollectorID), carrying the id of the collector
// whose served config produced the baseline. Project reads it so a beacon write
// can be attributed to a collector for reconciliation (#110). Absent on writes
// from a collector still running a pre-#110 baseline; Project then returns an
// empty collector id and the write is stored unattributed.
const CollectorIDLabel = "shepherd_collector_id"

// healthTypeHealthy is the health_type label value the pinned metric uses
// for "this component is up". Every other health_type value (unhealthy,
// exited, unknown — Alloy's component.HealthType enum) counts against
// Healthy below.
const healthTypeHealthy = "healthy"

// managedPipelinePrefix is the declare-label prefix internal/merge gives
// every pipeline it assembles (`declare "pipe_<sanitized>"`, instantiated as
// `pipe_<sanitized> "default" { }`). merge.SanitizeName emits only
// [a-z0-9_], so a pipeline's segment in a controller path is exactly
// "pipe_<sanitized>.default" and cutting at the first "." recovers the
// declare label. Restated rather than imported from internal/merge so this
// package depends only on the wire format.
const managedPipelinePrefix = "pipe_"

// ErrNoInstanceLabel is returned when a write request carries no `instance`
// label on any series recognized by Project. prometheus.scrape stamps this
// label on every series it scrapes (job/instance are its own target
// identity, not something this package or the baseline pipeline sets) —
// its absence means the request did not come from the rendered baseline
// pipeline, or arrived some other way this package does not understand.
// Without it there is no key to store the observations under, so Project
// refuses rather than inventing one.
var ErrNoInstanceLabel = errors.New("beacon: write request has no instance label on any recognized series")

// pipelineKey maps one loader's (controller_path, controller_id) constant
// labels to the managed pipeline it runs inside, or "" for a loader that is
// not inside one.
//
// Under remotecfg the shapes are (Alloy v1.20.1 / v1.18.1, verified in
// source; see project_test.go's ctrlSeries):
//
//	"/" + "remotecfg"                       -> "" (remotecfg root: the baseline, the pipe_x.default nodes)
//	"/remotecfg" + "pipe_x.default"         -> "pipe_x"
//	"/remotecfg/pipe_x.default" + "feat.y"  -> "pipe_x" (nested module rolled up)
//	"/" + ""                                -> "" (local bootstrap config)
//
// The FIRST segment starting with "pipe_" wins, so a pipeline that itself
// declares a "pipe_*" component still reports under its own name.
func pipelineKey(controllerPath, controllerID string) string {
	for _, seg := range strings.Split(path.Join(controllerPath, controllerID), "/") {
		if !strings.HasPrefix(seg, managedPipelinePrefix) {
			continue
		}
		name, _, _ := strings.Cut(seg, ".")
		return name
	}
	return ""
}

// Project reduces a decoded remote_write request to the identity (the
// `instance` label prometheus.scrape stamps on every series from one
// reporting Alloy process) and per-pipeline health of the managed pipelines
// that process ran, discarding every sample value once it has been read
// exactly once to decide a health bucket. See doc.go's "structural
// no-raw-sample guarantee" for what that promise rests on.
//
// A series whose __name__ is not runningComponentsMetric is skipped without
// its Samples ever being indexed — Project only ever reads a Sample.Value
// for the one metric it understands. A series from a loader outside any
// managed pipeline (pipelineKey == "") is dropped too.
func Project(wr *prompb.WriteRequest) (instanceLabel, collectorID string, observations []ComponentObservation, err error) {
	// Alloy emits one series per (loader, health_type) bucket that is
	// currently non-zero, and a pipeline may run several loaders (its own
	// plus one per nested module/custom component). So: first keep the
	// latest value per (loader, health_type) — a gauge re-reported within
	// one batch reflects its latest value, not an inflated sum — then sum
	// across the pipeline's loaders, whose buckets ARE additive.
	type loaderBucket struct{ pipeline, loader, healthType string }
	latest := make(map[loaderBucket]float64)

	for i := range wr.Timeseries {
		ts := &wr.Timeseries[i]

		var metricName, controllerPath, controllerID, healthType string
		for _, l := range ts.Labels {
			switch l.Name {
			case "__name__":
				metricName = l.Value
			case "controller_path":
				controllerPath = l.Value
			case "controller_id":
				controllerID = l.Value
			case "health_type":
				healthType = l.Value
			case "instance":
				if instanceLabel == "" {
					instanceLabel = l.Value
				}
			case CollectorIDLabel:
				if collectorID == "" {
					collectorID = l.Value
				}
			}
		}

		if metricName != runningComponentsMetric || healthType == "" || len(ts.Samples) == 0 {
			continue
		}
		pipeline := pipelineKey(controllerPath, controllerID)
		if pipeline == "" {
			// The remotecfg root loader (the baseline and the pipeline
			// instance nodes themselves), the local bootstrap config, or
			// anything else outside a managed pipeline.
			continue
		}
		key := loaderBucket{pipeline: pipeline, loader: path.Join(controllerPath, controllerID), healthType: healthType}
		latest[key] = ts.Samples[len(ts.Samples)-1].Value
	}

	if instanceLabel == "" {
		return "", "", nil, ErrNoInstanceLabel
	}

	// pipeline -> health_type -> count summed across the pipeline's loaders.
	counts := make(map[string]map[string]float64)
	for k, v := range latest {
		if counts[k.pipeline] == nil {
			counts[k.pipeline] = make(map[string]float64)
		}
		counts[k.pipeline][k.healthType] += v
	}

	order := make([]string, 0, len(counts))
	for pipeline := range counts {
		order = append(order, pipeline)
	}
	sort.Strings(order)
	observations = make([]ComponentObservation, 0, len(order))
	for _, pipeline := range order {
		observations = append(observations, ComponentObservation{
			ComponentName: pipeline,
			Healthy:       isHealthy(counts[pipeline]),
		})
	}
	return instanceLabel, collectorID, observations, nil
}

// isHealthy reports whether byHealthType — one pipeline's summed
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
