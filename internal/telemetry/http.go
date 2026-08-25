// Package telemetry holds the cross-cutting instrumentation Shepherd applies
// to its own serving paths: HTTP middleware, a Connect interceptor, and
// OpenTelemetry tracing setup.
//
// It is deliberately separate from internal/metrics, which only declares the
// metric variables. Keeping the declarations somewhere that imports nothing
// but Prometheus is what lets any package record a metric without dragging in
// chi, Connect, or the OTel SDK.
package telemetry

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"shepherd/internal/metrics"
)

// HTTPMetrics records request count and latency for every HTTP request.
//
// Before this, the management API had no operational telemetry of any kind:
// the only metrics were domain counters on the agent path, and the access log
// was emitted at Debug so a default `info` deployment produced no per-request
// record either. "Is the API up, and how slow is it" had no answer.
//
// Route labels come from chi's matched ROUTE PATTERN, not the URL. See
// routeLabel for why that distinction is load-bearing.
func HTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		route := routeLabel(r)
		metrics.HTTPRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
		metrics.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())

		// Rename the otelhttp span now that routing has happened. otelhttp
		// names the span before the router has matched, so without this every
		// span is called "shepherd" and a trace view groups unrelated
		// endpoints together. Setting http.route as well is what the semantic
		// conventions expect a server span to carry.
		if span := oteltrace.SpanFromContext(r.Context()); span.IsRecording() {
			span.SetName(r.Method + " " + route)
			span.SetAttributes(attribute.String("http.route", route))
		}
	})
}

// routeLabel returns the chi route pattern for a request, or "other" when
// nothing matched.
//
// This is the cardinality guard. The pattern is "/api/orgs/{org}/pipelines/{id}"
// no matter how many orgs and pipelines exist; labelling with r.URL.Path
// instead would mint a fresh time series per id and grow without bound —
// the standard way a /metrics endpoint becomes the thing that takes down
// Prometheus. Unmatched paths collapse to a single "other" series for the same
// reason: an unauthenticated scanner probing random URLs must not be able to
// create series at will.
//
// The pattern is only populated after the router has matched, which is why
// this is read AFTER next.ServeHTTP rather than before.
func routeLabel(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if p := rctx.RoutePattern(); p != "" {
			return p
		}
	}
	return "other"
}

// TraceHTTP wraps the whole router in an OpenTelemetry server span.
//
// It is applied outside chi rather than as chi middleware because the span has
// to exist before routing so that HTTPMetrics can rename it once the route
// pattern is known — otelhttp names a span before it can possibly know which
// handler will run.
//
// When tracing is disabled this is still in the chain, and still costs
// approximately nothing: with no provider installed the tracer is otel's
// no-op, so no span is allocated.
func TraceHTTP(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "shepherd",
		otelhttp.WithFilter(shouldTrace),
		// The span is renamed to "METHOD /route/{pattern}" by HTTPMetrics.
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method
		}),
	)
}

// shouldTrace filters out the traffic that would otherwise dominate a trace
// backend while telling nobody anything: probes fired every few seconds by
// Kubernetes, the metrics scrape, and static SPA assets. The application
// requests that remain are the ones anybody opens a trace view to look at.
func shouldTrace(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/metrics":
		return false
	}
	return !strings.HasPrefix(r.URL.Path, "/assets/")
}
