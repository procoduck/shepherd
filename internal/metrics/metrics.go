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

	// ServeRecomputeFailuresTotal counts failed serve-cache recomputes on both
	// serve paths (lazy GetConfig and eager recomputeOrgCaches), every
	// attempt; serve.RecordFailure increments it.
	ServeRecomputeFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "serve_recompute_failures_total",
		Help:      "Total number of serve-cache recompute failures (lazy and eager paths).",
	})

	// AgentRefusedTotal counts collector RPCs the agent API refused on an
	// identity check, by reason — a closed set (AgentRefused* below), so the
	// cardinality is bounded by construction.
	AgentRefusedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "agent_refused_total",
		Help:      "Total collector RPCs refused on an identity check, by reason (claim_denied, role_denied, unregister_denied, token_unbound, beacon_denied).",
	}, []string{"reason"})

	// BeaconDroppedTotal counts beacon writes dropped because they carry no
	// usable collector id, by reason — a closed set (BeaconDropped* below).
	// The writer still gets 204; nothing is stored.
	BeaconDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "beacon_dropped_total",
		Help:      "Total beacon writes dropped for carrying no usable collector id, by reason (collector_id_missing, collector_id_invalid, collector_id_mixed).",
	}, []string{"reason"})

	// SyncTotal counts gitsync reconciliation attempts by result.
	SyncTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "shepherd",
		Name:      "gitsync_total",
		Help:      "Total gitsync reconciliation attempts by result (ok, warning, error, link_gone).",
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
		Help:      "Current number of registered collector instances that checked in within agent.inactive_after (every registered instance when it is unset).",
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

// The shepherd_agent_refused_total reasons.
const (
	// AgentRefusedClaimDenied: the principal may not act for the cluster — a
	// binding's or token's cluster allowlist excludes it, the cluster is
	// claimed by another org, or the token asserts an org that does not exist.
	AgentRefusedClaimDenied = "claim_denied"
	// AgentRefusedRoleDenied: a binding's role allowlist excludes the role.
	AgentRefusedRoleDenied = "role_denied"
	// AgentRefusedUnregisterDenied: UnregisterCollector named an instance id
	// with rows outside the caller's org (or its allowlists). The caller still
	// gets success; those rows are left alone and only counted.
	AgentRefusedUnregisterDenied = "unregister_denied"
	// AgentRefusedTokenUnbound: an agent token with no organisation binding
	// (a token created before tokens were org-bound, or one whose org was
	// deleted). It authenticates but may act for nothing until an app admin
	// binds it.
	AgentRefusedTokenUnbound = "token_unbound"
	// AgentRefusedBeaconDenied: a beacon write from an org-bound agent token
	// named a collector whose cluster is not claimed by the token's org. The
	// write is dropped (the writer still gets 204) and only counted.
	AgentRefusedBeaconDenied = "beacon_denied"
)

// The shepherd_beacon_dropped_total reasons.
const (
	// BeaconDroppedCollectorIDMissing: no series in the write carried the
	// shepherd_collector_id label (a baseline from before it was stamped, or
	// a write the rendered baseline did not produce).
	BeaconDroppedCollectorIDMissing = "collector_id_missing"
	// BeaconDroppedCollectorIDInvalid: the shepherd_collector_id label is not
	// a UUID.
	BeaconDroppedCollectorIDInvalid = "collector_id_invalid"
	// BeaconDroppedCollectorIDMixed: the write's series carried more than one
	// distinct shepherd_collector_id, so it cannot be attributed to one
	// collector.
	BeaconDroppedCollectorIDMixed = "collector_id_mixed"
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
