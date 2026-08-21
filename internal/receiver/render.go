package receiver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Render validates cfg and produces the Alloy config text for the
// receiver-tier pipeline it describes. The output is raw pipeline contents —
// the same shape internal/wizard renderers produce — meant to be served
// wrapped the way internal/merge and internal/validate.WrapForValidation
// already wrap every pipeline before it reaches a collector or the real
// alloy binary.
func Render(cfg Config) (string, error) {
	if err := Validate(cfg); err != nil {
		return "", err
	}

	var sb strings.Builder
	first := true
	blank := func() {
		if !first {
			sb.WriteString("\n")
		}
		first = false
	}

	for _, p := range cfg.OTLP {
		blank()
		renderOTLPPipeline(&sb, p)
	}
	for i := range cfg.Faro {
		blank()
		renderFaroPipeline(&sb, &cfg.Faro[i])
	}

	return sb.String(), nil
}

func renderOTLPPipeline(sb *strings.Builder, p OTLPPipeline) {
	_, _ = fmt.Fprintf(sb, "otelcol.receiver.otlp %q {\n", p.Label)
	if p.GRPC != nil {
		_, _ = fmt.Fprintf(sb, "  grpc {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint               = %q\n", p.GRPC.ListenAddr)
		_, _ = fmt.Fprintf(sb, "    max_recv_msg_size      = %q\n", p.GRPC.MaxRecvMsgSize)
		_, _ = fmt.Fprintf(sb, "    max_concurrent_streams = %d\n", p.GRPC.MaxConcurrentStreams)
		_, _ = fmt.Fprintf(sb, "  }\n")
	}
	if p.HTTP != nil {
		_, _ = fmt.Fprintf(sb, "  http {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint               = %q\n", p.HTTP.ListenAddr)
		_, _ = fmt.Fprintf(sb, "    max_request_body_size  = %q\n", p.HTTP.MaxRequestBodySize)
		_, _ = fmt.Fprintf(sb, "  }\n")
	}
	// otelcol.receiver.otlp has no requests/sec throttle in the pinned schema
	// (internal/schema/artifacts) — only the size and concurrency caps set
	// above. Request-rate limiting for OTLP ingest has to happen upstream, at
	// the gateway or network layer; this pipeline does not and cannot
	// provide it.
	_, _ = fmt.Fprintf(sb, "  // no requests/sec limit is expressible here — see package doc.\n")
	_, _ = fmt.Fprintf(sb, "  output {\n")
	batchInput := fmt.Sprintf("otelcol.processor.batch.%s.input", p.Label)
	if p.Metrics != nil {
		_, _ = fmt.Fprintf(sb, "    metrics = [%s]\n", batchInput)
	}
	if p.Logs != nil {
		_, _ = fmt.Fprintf(sb, "    logs    = [%s]\n", batchInput)
	}
	if p.Traces != nil {
		_, _ = fmt.Fprintf(sb, "    traces  = [%s]\n", batchInput)
	}
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "}\n")

	sb.WriteString("\n")
	renderBatch(sb, p.Label, p.Batch, p.Metrics, p.Logs, p.Traces)

	for _, exp := range []*OTLPExporter{p.Metrics, p.Logs, p.Traces} {
		if exp == nil {
			continue
		}
		sb.WriteString("\n")
		renderOTLPExporter(sb, *exp)
	}
}

func renderBatch(sb *strings.Builder, label string, batch BatchConfig, metrics, logs, traces *OTLPExporter) {
	_, _ = fmt.Fprintf(sb, "otelcol.processor.batch %q {\n", label)
	if batch.Timeout != "" {
		_, _ = fmt.Fprintf(sb, "  timeout = %q\n", batch.Timeout)
	}
	if batch.SendBatchSize != 0 {
		_, _ = fmt.Fprintf(sb, "  send_batch_size = %d\n", batch.SendBatchSize)
	}
	if batch.SendBatchMaxSize != 0 {
		_, _ = fmt.Fprintf(sb, "  send_batch_max_size = %d\n", batch.SendBatchMaxSize)
	}
	_, _ = fmt.Fprintf(sb, "  output {\n")
	if metrics != nil {
		_, _ = fmt.Fprintf(sb, "    metrics = [%s]\n", otlpExporterInput(metrics))
	}
	if logs != nil {
		_, _ = fmt.Fprintf(sb, "    logs    = [%s]\n", otlpExporterInput(logs))
	}
	if traces != nil {
		_, _ = fmt.Fprintf(sb, "    traces  = [%s]\n", otlpExporterInput(traces))
	}
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "}\n")
}

func otlpExporterInput(exp *OTLPExporter) string {
	return fmt.Sprintf("%s.%s.input", otlpExporterComponent(exp.Protocol), exp.Name)
}

func renderOTLPExporter(sb *strings.Builder, exp OTLPExporter) {
	_, _ = fmt.Fprintf(sb, "%s %q {\n", otlpExporterComponent(exp.Protocol), exp.Name)
	headerLines := mergedHeaderLines(exp.Headers, exp.SecretHeaderEnv)
	if len(headerLines) > 0 {
		_, _ = fmt.Fprintf(sb, "  client {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint = %s\n", exp.EndpointExpr)
		_, _ = fmt.Fprintf(sb, "    headers = {\n")
		for _, line := range headerLines {
			_, _ = fmt.Fprintf(sb, "      %s\n", line)
		}
		_, _ = fmt.Fprintf(sb, "    }\n")
		_, _ = fmt.Fprintf(sb, "  }\n")
	} else {
		_, _ = fmt.Fprintf(sb, "  client {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint = %s\n", exp.EndpointExpr)
		_, _ = fmt.Fprintf(sb, "  }\n")
	}
	_, _ = fmt.Fprintf(sb, "}\n")
}

// mergedHeaderLines renders literal and secret-env headers as one
// deterministically ordered (sorted by header name) list of River map
// entries, e.g. `"X-Scope-OrgID" = "acme",`. Secret entries render as
// `sys.env("<VAR>")`, never a literal — Validate already refuses a header
// name present in both maps.
func mergedHeaderLines(literal, secretEnv map[string]string) []string {
	keys := make([]string, 0, len(literal)+len(secretEnv))
	for k := range literal {
		keys = append(keys, k)
	}
	for k := range secretEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := literal[k]; ok {
			lines = append(lines, fmt.Sprintf("%q = %q,", k, v))
			continue
		}
		lines = append(lines, fmt.Sprintf("%q = sys.env(%q),", k, secretEnv[k]))
	}
	return lines
}

func renderFaroPipeline(sb *strings.Builder, p *FaroPipeline) {
	_, _ = fmt.Fprintf(sb, "faro.receiver %q {\n", p.Label)
	_, _ = fmt.Fprintf(sb, "  server {\n")
	_, _ = fmt.Fprintf(sb, "    listen_address = %q\n", p.Server.ListenAddr)
	_, _ = fmt.Fprintf(sb, "    listen_port    = %d\n", p.Server.ListenPort)
	_, _ = fmt.Fprintf(sb, "    max_allowed_payload_size = %q\n", p.Server.MaxAllowedPayloadSize)
	renderCORS(sb, p.CORS)
	renderRateLimit(sb, p.Server.RateLimit)
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "  output {\n")
	if p.Logs != nil {
		_, _ = fmt.Fprintf(sb, "    logs   = [loki.write.%s.receiver]\n", p.Logs.Name)
	}
	if p.Traces != nil {
		_, _ = fmt.Fprintf(sb, "    traces = [otelcol.processor.batch.%s.input]\n", faroTracesBatchLabel(p.Label))
	}
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "}\n")

	if p.Logs != nil {
		sb.WriteString("\n")
		renderLokiExporter(sb, *p.Logs)
	}
	if p.Traces != nil {
		sb.WriteString("\n")
		renderBatch(sb, faroTracesBatchLabel(p.Label), BatchConfig{}, nil, nil, p.Traces)
		sb.WriteString("\n")
		renderOTLPExporter(sb, *p.Traces)
	}
}

// renderCORS is the one function in this package that decides what
// cors_allowed_origins the receiver-tier pipeline emits. See CORSPolicy and
// validateCORS: by the time this runs, Validate has already refused a
// literal "*" inside Origins and an AllowAll+Origins conflict, so the two
// branches below are the only two ways a non-empty allowlist reaches the
// output, and both are named, deliberate call sites.
func renderCORS(sb *strings.Builder, c CORSPolicy) {
	_, _ = fmt.Fprintf(sb, "    // CORS answers docs/gateway-tier-plan.md D2: the Gateway API Standard\n")
	_, _ = fmt.Fprintf(sb, "    // channel carries no CORS filter at the pinned floor, so this receiver is\n")
	_, _ = fmt.Fprintf(sb, "    // the only place origin is enforced for browser traffic.\n")
	switch {
	case c.AllowAll:
		_, _ = fmt.Fprintf(sb, "    // deliberate wildcard: CORSPolicy.AllowAll was set explicitly.\n")
		_, _ = fmt.Fprintf(sb, "    cors_allowed_origins = [\"*\"]\n")
	case len(c.Origins) == 0:
		_, _ = fmt.Fprintf(sb, "    // no origins configured: CORS is disabled (Alloy's own default for []).\n")
		_, _ = fmt.Fprintf(sb, "    cors_allowed_origins = []\n")
	default:
		_, _ = fmt.Fprintf(sb, "    cors_allowed_origins = %s\n", renderStringList(c.Origins))
	}
}

func renderRateLimit(sb *strings.Builder, rl RateLimit) {
	_, _ = fmt.Fprintf(sb, "    rate_limiting {\n")
	_, _ = fmt.Fprintf(sb, "      enabled = %t\n", rl.Enabled)
	if rl.Enabled {
		_, _ = fmt.Fprintf(sb, "      strategy   = %q\n", rl.Strategy)
		_, _ = fmt.Fprintf(sb, "      rate       = %d\n", rl.Rate)
		_, _ = fmt.Fprintf(sb, "      burst_size = %d\n", rl.BurstSize)
	}
	_, _ = fmt.Fprintf(sb, "    }\n")
}

func renderLokiExporter(sb *strings.Builder, exp LokiExporter) {
	_, _ = fmt.Fprintf(sb, "loki.write %q {\n", exp.Name)
	_, _ = fmt.Fprintf(sb, "  endpoint {\n")
	_, _ = fmt.Fprintf(sb, "    url       = %s\n", exp.URLExpr)
	_, _ = fmt.Fprintf(sb, "    tenant_id = %q\n", exp.TenantID)
	if exp.BearerTokenEnv != "" {
		_, _ = fmt.Fprintf(sb, "    bearer_token = sys.env(%q)\n", exp.BearerTokenEnv)
	}
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "}\n")
}

func renderStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
