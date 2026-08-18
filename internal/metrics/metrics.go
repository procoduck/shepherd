// Package metrics registers and exposes Prometheus metrics for Shepherd itself.
package metrics

import (
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
)
