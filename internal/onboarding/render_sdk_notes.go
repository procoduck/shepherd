package onboarding

import (
	"fmt"
	"strings"
)

// renderSDKNotes renders a Markdown document explaining the env vars this
// package emits: why they are enough for most SDKs (auto-instrumentation
// reads them with no code change), the base-vs-per-signal endpoint
// boundary a caller hits the moment they need an OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT
// override, why OTEL_EXPORTER_OTLP_PROTOCOL must be set explicitly rather
// than left to each SDK's own default, and D10's documented Faro+OTLP
// hybrid — as prose, since there is no faro.receiver support to render a
// snippet against yet (see doc.go).
func renderSDKNotes(base string, spec ConnectAppSpec) (string, error) {
	tracesEP, err := SignalEndpoint(base, SignalTraces)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "# Connecting %q\n\n", spec.ServiceName)
	_, _ = fmt.Fprintf(&sb, "Route: kind=%s segment=%s\n\n", spec.Route.Kind, spec.Route.RouteSegment)

	_, _ = fmt.Fprintf(&sb, "## Most SDKs need no code change\n\n")
	_, _ = fmt.Fprintf(&sb, "Every official OpenTelemetry SDK auto-configures its OTLP exporter from\n")
	_, _ = fmt.Fprintf(&sb, "environment variables. Setting the three in the rendered `.env` file\n")
	_, _ = fmt.Fprintf(&sb, "(`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`,\n")
	_, _ = fmt.Fprintf(&sb, "`OTEL_SERVICE_NAME`) before your process starts is enough for traces,\n")
	_, _ = fmt.Fprintf(&sb, "metrics, and logs to start flowing, using whatever auto-instrumentation\n")
	_, _ = fmt.Fprintf(&sb, "your language's SDK ships (`opentelemetry-instrument` for Python, the\n")
	_, _ = fmt.Fprintf(&sb, "`@opentelemetry/auto-instrumentations-node` package for Node.js, a Java\n")
	_, _ = fmt.Fprintf(&sb, "agent jar, and so on) or your own manual SDK initialization — both read\n")
	_, _ = fmt.Fprintf(&sb, "the same env vars.\n\n")

	_, _ = fmt.Fprintf(&sb, "## Always set OTEL_EXPORTER_OTLP_PROTOCOL explicitly\n\n")
	_, _ = fmt.Fprintf(&sb, "Several SDKs (the Go, Java, and Python SDKs among them) default to\n")
	_, _ = fmt.Fprintf(&sb, "OTLP/gRPC when this variable is unset. This gateway's tenant routing is\n")
	_, _ = fmt.Fprintf(&sb, "path-prefix based (docs/gateway-tier-plan.md D9) and gRPC's request path\n")
	_, _ = fmt.Fprintf(&sb, "is fixed by the service definition, not configurable — so a client left on\n")
	_, _ = fmt.Fprintf(&sb, "its gRPC default will never reach a route this package's endpoint\n")
	_, _ = fmt.Fprintf(&sb, "describes; it dials the right host but sends a request path no HTTPRoute\n")
	_, _ = fmt.Fprintf(&sb, "matches. The rendered `%s` value is required, not\n", "OTEL_EXPORTER_OTLP_PROTOCOL")
	_, _ = fmt.Fprintf(&sb, "optional, for exactly this reason.\n\n")

	_, _ = fmt.Fprintf(&sb, "## Base endpoint vs. per-signal endpoint\n\n")
	_, _ = fmt.Fprintf(&sb, "`OTEL_EXPORTER_OTLP_ENDPOINT` is a BASE endpoint:\n\n")
	_, _ = fmt.Fprintf(&sb, "    %s\n\n", base)
	_, _ = fmt.Fprintf(&sb, "The OTLP/HTTP exporter appends `v1/traces`, `v1/metrics`, `v1/logs` to it\n")
	_, _ = fmt.Fprintf(&sb, "itself. If your setup instead needs a signal-specific override —\n")
	_, _ = fmt.Fprintf(&sb, "`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` and friends — those variables are\n")
	_, _ = fmt.Fprintf(&sb, "used exactly as given, with nothing appended, so it must already include\n")
	_, _ = fmt.Fprintf(&sb, "the signal path, e.g. for traces:\n\n")
	_, _ = fmt.Fprintf(&sb, "    OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=%s\n\n", tracesEP)
	_, _ = fmt.Fprintf(&sb, "Only set a per-signal override if you have a reason to (routing one\n")
	_, _ = fmt.Fprintf(&sb, "signal differently); otherwise the base endpoint alone already covers all\n")
	_, _ = fmt.Fprintf(&sb, "three.\n\n")

	_, _ = fmt.Fprintf(&sb, "## The route segment is an identifier, not a secret\n\n")
	_, _ = fmt.Fprintf(&sb, "docs/gateway-tier-plan.md §3: the segment in this URL identifies your\n")
	_, _ = fmt.Fprintf(&sb, "tenant to the gateway; it is not an authorization token. The gateway's\n")
	_, _ = fmt.Fprintf(&sb, "real controls are origin allowlists, rate limits, and rotation — treat\n")
	_, _ = fmt.Fprintf(&sb, "the endpoint like any other config value, and rotate it (ask your\n")
	_, _ = fmt.Fprintf(&sb, "platform team) if you believe it has leaked somewhere sensitive.\n\n")

	_, _ = fmt.Fprintf(&sb, "## Browser RUM (Grafana Faro)\n\n")
	_, _ = fmt.Fprintf(&sb, "Shepherd does not yet run a Faro receiver (docs/gateway-tier-plan.md D10\n")
	_, _ = fmt.Fprintf(&sb, "— it is demand-driven, not built). If you want browser RUM (error\n")
	_, _ = fmt.Fprintf(&sb, "capture, Web Vitals, session tracking) today, D10 documents a hybrid that\n")
	_, _ = fmt.Fprintf(&sb, "works without it: use the Faro Web SDK for the RUM batteries, but\n")
	_, _ = fmt.Fprintf(&sb, "configure ITS trace exporter (`@grafana/faro-web-tracing`, which already\n")
	_, _ = fmt.Fprintf(&sb, "speaks OTLP) to point at the OTLP endpoint above instead of a Faro\n")
	_, _ = fmt.Fprintf(&sb, "collector endpoint. Traces ride this scalable, gateway-tenanted path; ask\n")
	_, _ = fmt.Fprintf(&sb, "your platform team when Faro support ships if you need the RUM payload\n")
	_, _ = fmt.Fprintf(&sb, "(errors/logs/vitals) ingested too.\n")

	return sb.String(), nil
}
