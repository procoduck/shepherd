package onboarding

import (
	"fmt"
	"net/url"
	"strings"

	"shepherd/internal/gateway"
)

// Signal is an OTLP signal type.
type Signal string

// The three OTLP signals a receiver-tier pipeline can carry (internal/receiver.Config).
const (
	SignalTraces  Signal = "traces"
	SignalMetrics Signal = "metrics"
	SignalLogs    Signal = "logs"
)

// signalPath maps a Signal to the path segment OTLP/HTTP appends to a base
// endpoint for that signal, per the OpenTelemetry specification's OTLP
// exporter configuration doc
// (https://opentelemetry.io/docs/specs/otel/protocol/exporter/, verified
// 2026-08-22): "OTEL_EXPORTER_OTLP_ENDPOINT is used as a base URL and the
// signals are sent to these paths relative to that: Traces: v1/traces,
// Metrics: v1/metrics, Logs: v1/logs." This is the single source both
// BaseEndpoint (which must NOT append one of these) and SignalEndpoint
// (which must) draw from — see doc.go's "compatibility boundary" section.
var signalPath = map[Signal]string{
	SignalTraces:  "v1/traces",
	SignalMetrics: "v1/metrics",
	SignalLogs:    "v1/logs",
}

// BaseEndpoint returns the OTLP base endpoint for route: gatewayBaseURL with
// route's actual routed path prefix appended, and nothing else.
//
// This is the ONLY place in this package that computes a route's path, and
// it does so by calling route.PathPrefix() — internal/gateway/route.go's own
// definition, the exact string gateway.RenderHTTPRoute renders into the
// HTTPRoute's path match — rather than restating "/" + kind + "/" + segment
// as a local format string here. See TestBaseEndpointPathMatchesRenderedRoute
// for the proof this cannot silently diverge from what the gateway actually
// routes (plan §5's W7 must-not: "must not emit an endpoint that no route
// serves").
//
// The result is deliberately the BASE form, with no per-signal suffix: per
// the OpenTelemetry spec (see signalPath's doc comment), OTLP/HTTP SDKs
// append v1/traces etc. to OTEL_EXPORTER_OTLP_ENDPOINT themselves. Use
// SignalEndpoint for the form that needs the suffix already applied.
func BaseEndpoint(gatewayBaseURL string, route gateway.RouteSpec) (string, error) {
	if route.Kind != gateway.KindOTLP {
		return "", fmt.Errorf("onboarding: route kind %q is not %q — this package renders OTLP/HTTP "+
			"artifacts only (see package doc for why gRPC and Faro are both excluded)", route.Kind, gateway.KindOTLP)
	}
	if err := gateway.ValidateSegment(route.RouteSegment); err != nil {
		return "", fmt.Errorf("onboarding: route segment: %w", err)
	}
	base, err := parseGatewayBaseURL(gatewayBaseURL)
	if err != nil {
		return "", err
	}
	// route.PathPrefix() always starts with "/" (route.go: "/" + Kind +
	// "/" + RouteSegment), so trimming any trailing slash off the base's
	// existing path before concatenating is enough to avoid a doubled "//".
	base.Path = strings.TrimSuffix(base.Path, "/") + route.PathPrefix()
	return base.String(), nil
}

// SignalEndpoint returns the fully-qualified, signal-specific OTLP endpoint:
// base (as returned by BaseEndpoint) with signal's path appended.
//
// Use this ONLY for a tool that will NOT itself append a signal path — the
// per-signal environment variables (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and
// friends) are specified to be used "as-is without any modification" (see
// signalPath's doc comment), so if this package handed one of those a bare
// base endpoint, the SDK would send that signal to the gateway's bare tenant
// prefix — which internal/gateway.RenderHTTPRoute never routes anywhere on
// its own; only "<prefix>/v1/<signal>" survives the URLRewrite into
// something the receiver tier's otelcol.receiver.otlp actually expects.
func SignalEndpoint(base string, signal Signal) (string, error) {
	suffix, ok := signalPath[signal]
	if !ok {
		return "", fmt.Errorf("onboarding: unknown signal %q (want %q, %q, or %q)", signal, SignalTraces, SignalMetrics, SignalLogs)
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("onboarding: parsing base endpoint %q: %w", base, err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + suffix
	return u.String(), nil
}

// parseGatewayBaseURL parses and validates raw as a gateway base URL: must
// be non-empty, must parse, must be https (the tenant route segment and
// every signal it carries travel in the URL and in request bodies — sending
// either over plaintext is not a choice this package will render), and must
// name a host.
func parseGatewayBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("onboarding: gateway base URL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("onboarding: parsing gateway base URL %q: %w", raw, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("onboarding: gateway base URL %q must use https, got scheme %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("onboarding: gateway base URL %q has no host", raw)
	}
	return u, nil
}
