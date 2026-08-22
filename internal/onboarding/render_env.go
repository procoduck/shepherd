package onboarding

import (
	"fmt"
	"strings"
)

// renderEnv renders coreOTLPEnv as a plain KEY=VALUE .env file: the form a
// local dev shell (`export $(cat .env)`), `docker run --env-file`, or a
// Lambda console's "paste as text" environment editor all accept directly.
func renderEnv(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	writeEnvFileHeader(&sb, spec)
	for _, v := range coreOTLPEnv(base, spec) {
		_, _ = fmt.Fprintf(&sb, "%s=%s\n", v.Key, v.Value)
	}
	return sb.String()
}

// writeEnvFileHeader writes the comment block every env-file-shaped
// renderer in this package starts with: what tenant route this connects to,
// and the compatibility boundary a reader needs before adding any env var
// this package did not already render (doc.go's "compatibility boundary"
// section, in miniature, at the point of use).
func writeEnvFileHeader(sb *strings.Builder, spec ConnectAppSpec) {
	_, _ = fmt.Fprintf(sb, "# Shepherd OTLP ingest for %q, route segment %q.\n", spec.ServiceName, spec.Route.RouteSegment)
	_, _ = fmt.Fprintf(sb, "#\n")
	_, _ = fmt.Fprintf(sb, "# OTEL_EXPORTER_OTLP_ENDPOINT is a BASE endpoint: the OTLP/HTTP exporter\n")
	_, _ = fmt.Fprintf(sb, "# appends v1/traces, v1/metrics, v1/logs itself. Do not append a signal path\n")
	_, _ = fmt.Fprintf(sb, "# to it, and do not set an OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT override\n")
	_, _ = fmt.Fprintf(sb, "# without also appending that signal's path — the per-signal variables are\n")
	_, _ = fmt.Fprintf(sb, "# used exactly as given, with nothing appended.\n")
	_, _ = fmt.Fprintf(sb, "#\n")
	_, _ = fmt.Fprintf(sb, "# The route segment in this endpoint identifies your tenant to the gateway;\n")
	_, _ = fmt.Fprintf(sb, "# it is not a secret credential (docs/gateway-tier-plan.md §3) — treat it\n")
	_, _ = fmt.Fprintf(sb, "# like any other endpoint URL in your config, not like an API key.\n")
}
