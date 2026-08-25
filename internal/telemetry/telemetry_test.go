package telemetry_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/telemetry"
)

// The route label is the cardinality guard: it must be the chi ROUTE PATTERN,
// never the concrete URL. Labelling by URL would mint a new time series per
// org and per pipeline id, which is the standard way a /metrics endpoint
// becomes the thing that takes Prometheus down.
func TestHTTPMetricsLabelsByRoutePatternNotURL(t *testing.T) {
	r := chi.NewRouter()
	r.Use(telemetry.HTTPMetrics)
	r.Get("/api/orgs/{org}/pipelines/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Three different concrete paths through one route.
	for _, path := range []string{
		"/api/orgs/org-a/pipelines/p-1",
		"/api/orgs/org-b/pipelines/p-2",
		"/api/orgs/org-c/pipelines/p-3",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d", path, rec.Code)
		}
	}

	const pattern = "/api/orgs/{org}/pipelines/{id}"
	if got := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues(http.MethodGet, pattern, "200")); got != 3 {
		t.Errorf("three requests through one route should share one series, got %v on the pattern label", got)
	}
	// And the concrete ids must not have produced series of their own.
	for _, id := range []string{"p-1", "p-2", "p-3"} {
		series := testutil.CollectAndCount(metrics.HTTPRequestsTotal)
		if series > 8 { // generous: other tests in this package add a few
			t.Fatalf("unexpected series growth (%d) — a per-id label would look like this", series)
		}
		_ = id
	}
}

// An unmatched path must collapse to one "other" series rather than creating
// one per URL: /metrics is reachable by anything that can reach the server, so
// an unauthenticated scanner must not be able to mint series at will.
func TestHTTPMetricsCollapsesUnmatchedRoutes(t *testing.T) {
	r := chi.NewRouter()
	r.Use(telemetry.HTTPMetrics)
	// A real route as well as the NotFound handler: chi only builds its
	// middleware chain once at least one route is registered, so a router with
	// nothing but NotFound bypasses middleware entirely and would make this
	// test pass for the wrong reason.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	before := testutil.CollectAndCount(metrics.HTTPRequestsTotal)
	for _, path := range []string{"/wp-admin", "/.env", "/random/" + strings.Repeat("x", 40)} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	after := testutil.CollectAndCount(metrics.HTTPRequestsTotal)

	if after-before > 1 {
		t.Errorf("three unmatched paths created %d new series; they must collapse to one \"other\"", after-before)
	}
	if got := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues(http.MethodGet, "other", "404")); got != 3 {
		t.Errorf("expected 3 requests on the \"other\" series, got %v", got)
	}
}

// A failing RPC must still be counted, and labelled with its Connect code —
// an auth-failure or permission-denied spike is precisely what a graph is for.
//
// connect.AnyRequest is a sealed interface (it carries an unexported method),
// so a test cannot hand-build one with a chosen procedure; connect.NewRequest
// produces an empty Spec. That is fine for what this asserts, which is the
// code-labelling logic rather than the procedure string. The procedure label
// on the real mount is covered by scraping a running server.
func TestInterceptorCountsFailuresByCode(t *testing.T) {
	interceptor := telemetry.Interceptor()

	failing := interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errBoom)
	})
	succeeding := interceptor(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&struct{}{}), nil
	})

	req := connect.NewRequest(&struct{}{})
	procedure := req.Spec().Procedure

	deniedBefore := testutil.ToFloat64(metrics.RPCRequestsTotal.WithLabelValues(procedure, "permission_denied"))
	okBefore := testutil.ToFloat64(metrics.RPCRequestsTotal.WithLabelValues(procedure, "ok"))

	if _, err := failing(context.Background(), req); err == nil {
		t.Fatal("expected the inner handler's error to propagate")
	}
	if _, err := succeeding(context.Background(), connect.NewRequest(&struct{}{})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := testutil.ToFloat64(metrics.RPCRequestsTotal.WithLabelValues(procedure, "permission_denied")); got-deniedBefore != 1 {
		t.Errorf("failed RPC not counted under its code (delta %v)", got-deniedBefore)
	}
	// connect.CodeOf(nil) reports CodeUnknown, so success has to be labelled
	// explicitly rather than by asking for the error's code.
	if got := testutil.ToFloat64(metrics.RPCRequestsTotal.WithLabelValues(procedure, "ok")); got-okBefore != 1 {
		t.Errorf("successful RPC should be labelled \"ok\" (delta %v)", got-okBefore)
	}
}

var errBoom = errors.New("boom")

// InitTracing must actually install a provider when an endpoint is configured.
//
// This exists because the first real run failed with "conflicting Schema URL"
// — resource.Merge refuses two different schema versions — and InitTracing
// treats a setup failure as non-fatal, so tracing silently stayed off while
// the deployment looked configured. A test that only asserted "no panic"
// would have passed.
func TestInitTracingInstallsAProvider(t *testing.T) {
	cfg := &config.Config{Tracing: config.TracingConfig{
		Enabled: true,
		// Not dialled at Init: the OTLP gRPC exporter connects lazily, so no
		// collector needs to exist for this to exercise provider construction.
		Endpoint:    "127.0.0.1:4317",
		Protocol:    "grpc",
		Insecure:    true,
		SampleRatio: 1.0,
		ServiceName: "shepherd-test",
	}}
	shutdown, err := telemetry.InitTracing(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("InitTracing returned an error, so tracing would be silently off: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) }) //nolint:errcheck // best-effort in cleanup

	tracer := otel.Tracer("probe")
	_, span := tracer.Start(context.Background(), "probe-span")
	defer span.End()
	if !span.SpanContext().IsValid() {
		t.Error("no real tracer provider installed: spans are no-ops despite an endpoint being configured")
	}
	if !span.IsRecording() {
		t.Error("span is not recording at sample_ratio 1.0")
	}
}

// Enabled without an endpoint must be reported, not silently ignored: an
// operator who switched tracing on is expecting spans, and an empty trace
// backend with nothing in the logs is the worst way to learn otherwise.
func TestInitTracingEnabledWithoutEndpointIsAnError(t *testing.T) {
	cfg := &config.Config{Tracing: config.TracingConfig{Enabled: true}}
	shutdown, err := telemetry.InitTracing(context.Background(), cfg, slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("enabled tracing with no endpoint must report a misconfiguration")
	}
	if shutdown == nil {
		t.Fatal("shutdown must stay non-nil even on error so callers can defer it")
	}
}

// With tracing disabled, it must be genuinely free — a no-op tracer, not a
// provider quietly buffering spans nobody collects.
func TestInitTracingDisabledByDefault(t *testing.T) {
	shutdown, err := telemetry.InitTracing(context.Background(), &config.Config{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("disabled tracing must not error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil so callers can defer it unconditionally")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("no-op shutdown returned an error: %v", err)
	}
}

// The span must be named for the matched ROUTE, not the raw method.
//
// otelhttp names a span before chi has routed, so without the rename in
// HTTPMetrics every server span is called "GET" and a trace view groups
// unrelated endpoints under one name — which is the difference between
// tracing being useful and being decorative. Health probes and static assets
// must produce no span at all, or the backend fills with Kubernetes liveness
// checks.
func TestTraceHTTPNamesSpansByRouteAndFiltersNoise(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) }) //nolint:errcheck // best-effort
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	r := chi.NewRouter()
	r.Use(telemetry.HTTPMetrics)
	r.Get("/api/orgs/{org}/pipelines/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := telemetry.TraceHTTP(r)

	for _, path := range []string{"/api/orgs/org-a/pipelines/p-1", "/healthz", "/assets/index-abc123.js"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		var names []string
		for _, s := range spans {
			names = append(names, s.Name())
		}
		t.Fatalf("expected exactly 1 span (health probes and assets filtered), got %d: %v", len(spans), names)
	}
	const want = "GET /api/orgs/{org}/pipelines/{id}"
	if got := spans[0].Name(); got != want {
		t.Errorf("span name = %q, want %q — the route rename did not happen", got, want)
	}
	var sawRoute bool
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "http.route" {
			sawRoute = true
			if attr.Value.AsString() != "/api/orgs/{org}/pipelines/{id}" {
				t.Errorf("http.route = %q", attr.Value.AsString())
			}
		}
	}
	if !sawRoute {
		t.Error("span carries no http.route attribute")
	}
}
