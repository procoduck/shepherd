// Package metrics registers and exposes Prometheus metrics for Shepherd itself.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
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
)

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
