// Package metrics registers and exposes Prometheus metrics for Shepherd itself.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"shepherd/internal/version"
)

var (
	// BuildInfo exposes the running build's version and commit as label values
	// with a constant value of 1 — the conventional `*_build_info` pattern, so
	// a dashboard can join the version onto any other Shepherd series and an
	// operator can confirm what a pod actually rolled to. Set once in init from
	// the ldflags-stamped internal/version vars.
	BuildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "shepherd",
		Name:      "build_info",
		Help:      "Build information; constant 1, the version and commit labels carry the values.",
	}, []string{"version", "commit"})

	// GetConfigTotal counts GetConfig RPCs by result label.
	GetConfigTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "getconfig_total",
		Help:      "Total number of GetConfig RPCs handled, labelled by result.",
	}, []string{"result"})

	// GetConfigDuration tracks GetConfig RPC latency.
	GetConfigDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "shepherd",
		Name:      "getconfig_duration_seconds",
		Help:      "GetConfig RPC duration in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"result"})

	// ServeRecomputeFailuresTotal counts failures while lazily recomputing serve caches.
	ServeRecomputeFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "serve_recompute_failures_total",
		Help:      "Total number of lazy serve-cache recompute failures.",
	})

	// SyncTotal counts gitsync reconciliation attempts by result.
	SyncTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "gitsync_total",
		Help:      "Total gitsync reconciliation attempts by result (ok, error).",
	}, []string{"result"})

	// ValidationTotal counts pipeline validation requests by stage and result.
	ValidationTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "validation_total",
		Help:      "Total pipeline validation requests by stage and result.",
	}, []string{"stage", "result"})

	// ActiveCollectors tracks the current number of live collector instances.
	ActiveCollectors = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "shepherd",
		Name:      "active_collectors",
		Help:      "Current number of non-inactive, non-unregistered collector instances.",
	})

	// HTTPRequestsTotal counts HTTP requests to the management surface.
	//
	// The `route` label is the chi ROUTE PATTERN ("/api/orgs/{org}/pipelines"),
	// never the concrete path. A label built from the raw URL would mint a new
	// time series per org and per pipeline id, which is how a metrics endpoint
	// takes down the Prometheus scraping it.
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests by method, route pattern, and status class.",
	}, []string{"method", "route", "code"})

	// HTTPRequestDuration tracks HTTP handler latency by route pattern.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "shepherd",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request duration in seconds by method and route pattern.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})

	// RPCRequestsTotal counts Connect RPCs by procedure and result code.
	// Procedure names are a closed set generated from the protos, so the
	// cardinality is bounded by construction.
	RPCRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "rpc_requests_total",
		Help:      "Total Connect RPCs by procedure and Connect error code (\"ok\" when successful).",
	}, []string{"procedure", "code"})

	// RPCDuration tracks Connect RPC latency by procedure.
	RPCDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "shepherd",
		Name:      "rpc_duration_seconds",
		Help:      "Connect RPC duration in seconds by procedure.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"procedure"})

	// PipelineMatchChangesTotal counts pipeline-to-collector match flips caused
	// by a label mutation, labelled by direction ("added" or "removed"). A
	// collector's effective label set can change which pipelines it draws
	// config from without anyone editing a pipeline — this is the signal an
	// operator's existing Alertmanager/Grafana can alert on directly (e.g.
	// increase(...{direction="removed"}[1h]) > 0), no new alerting UI needed
	// inside Shepherd. See LABEL-MATCHING-PLAN.md §7.
	PipelineMatchChangesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "pipeline_match_changes_total",
		Help:      "Total pipeline-to-collector match changes caused by a label mutation, by direction (added, removed).",
	}, []string{"direction"})
)

// init publishes the build-info series as soon as the package loads, so
// `shepherd_build_info` is present on the very first scrape without any
// startup wiring having to remember to set it.
func init() {
	BuildInfo.WithLabelValues(version.Version, version.Commit).Set(1)
}

// ObserveGetConfig records one GetConfig outcome: the counter and the latency
// histogram together.
//
// It exists because the two were separated once already — the counter was
// incremented at five return points and the histogram was declared and never
// observed at any of them, so `shepherd_getconfig_duration_seconds` did not
// appear in /metrics at all. One call recording both is the shape that cannot
// drift apart again.
func ObserveGetConfig(result string, start time.Time) {
	GetConfigTotal.WithLabelValues(result).Inc()
	GetConfigDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
}

// ObserveValidation records one validation stage outcome. Called from inside
// internal/validate so every caller — the management API, the agent path, the
// gitsync reconciler, and the CLI — is counted without each having to
// remember to.
func ObserveValidation(stage string, valid bool) {
	result := "invalid"
	if valid {
		result = "valid"
	}
	ValidationTotal.WithLabelValues(stage, result).Inc()
}
