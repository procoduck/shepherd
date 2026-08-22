package receiver

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"shepherd/internal/gateway"
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
	passThrough := p.Mode == TenancyPassThrough
	var authLabel string
	if passThrough {
		authLabel = otlpTenantAuthLabel(p.Label)
	}

	_, _ = fmt.Fprintf(sb, "otelcol.receiver.otlp %q {\n", p.Label)
	if p.GRPC != nil {
		_, _ = fmt.Fprintf(sb, "  grpc {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint               = %q\n", p.GRPC.ListenAddr)
		_, _ = fmt.Fprintf(sb, "    max_recv_msg_size      = %q\n", p.GRPC.MaxRecvMsgSize)
		_, _ = fmt.Fprintf(sb, "    max_concurrent_streams = %d\n", p.GRPC.MaxConcurrentStreams)
		if passThrough {
			// D10: without this, the gateway-injected tenant header never
			// reaches otelcol.auth.headers' from_context below — see
			// TenancyPassThrough's doc comment.
			_, _ = fmt.Fprintf(sb, "    include_metadata       = true\n")
		}
		_, _ = fmt.Fprintf(sb, "  }\n")
	}
	if p.HTTP != nil {
		_, _ = fmt.Fprintf(sb, "  http {\n")
		_, _ = fmt.Fprintf(sb, "    endpoint               = %q\n", p.HTTP.ListenAddr)
		_, _ = fmt.Fprintf(sb, "    max_request_body_size  = %q\n", p.HTTP.MaxRequestBodySize)
		if passThrough {
			_, _ = fmt.Fprintf(sb, "    include_metadata       = true\n")
		}
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

	if passThrough {
		sb.WriteString("\n")
		renderOTLPTenantAuth(sb, authLabel)
	}

	sb.WriteString("\n")
	renderBatch(sb, p.Label, p.Batch, p.Mode == TenancyPassThrough, p.Metrics, p.Logs, p.Traces)

	for _, exp := range []*OTLPExporter{p.Metrics, p.Logs, p.Traces} {
		if exp == nil {
			continue
		}
		sb.WriteString("\n")
		authRef := ""
		if passThrough {
			authRef = fmt.Sprintf("otelcol.auth.headers.%s.handler", authLabel)
		}
		renderOTLPExporter(sb, *exp, authRef)
	}
}

// otlpTenantAuthLabel derives the otelcol.auth.headers component label for a
// pass-through OTLP pipeline from its own Label — deterministic and, because
// pipeline Labels are already required unique (Validate's claim map), never
// collides across pipelines without a caller needing to name it separately.
func otlpTenantAuthLabel(pipelineLabel string) string {
	return "otlp_" + pipelineLabel + "_tenant"
}

// renderOTLPTenantAuth renders the one otelcol.auth.headers component a
// pass-through OTLP pipeline shares across all of its exporters. See
// TenancyPassThrough's doc comment for what include_metadata and this
// component together guarantee, and what they do not: this component trusts
// that whatever set gateway.TenantHeader on the inbound request is the real
// gateway (internal/gateway.RenderHTTPRoute, which SETS — never appends —
// the header, so a client cannot smuggle its own value past it). A
// pass-through pipeline reachable by any path that is not fronted by that
// gateway has no tenant isolation at all — this component cannot supply one.
//
// from_context is lowercased for readability, not for correctness: the
// lookup is case-insensitive on both sides. Verified against upstream source
// on 2026-08-21 rather than assumed — opentelemetry-collector's client.go
// stores metadata keys via strings.ToLower on insert (NewMetadata) and
// lowercases the key again on read (Metadata.Get), so "X-Scope-OrgID" and
// "x-scope-orgid" resolve to the same entry.
func renderOTLPTenantAuth(sb *strings.Builder, label string) {
	_, _ = fmt.Fprintf(sb, "// D10 pass-through tenancy for this OTLP pipeline: %s trusts that the\n", gateway.TenantHeader)
	_, _ = fmt.Fprintf(sb, "// gateway tier (internal/gateway.RenderHTTPRoute) already set %s on the\n", gateway.TenantHeader)
	_, _ = fmt.Fprintf(sb, "// inbound request — it does not and cannot verify who set it. This\n")
	_, _ = fmt.Fprintf(sb, "// component reads that value out of the connection context (populated by\n")
	_, _ = fmt.Fprintf(sb, "// include_metadata = true above) and sets it on every outbound request\n")
	_, _ = fmt.Fprintf(sb, "// this pipeline's exporters make. It guarantees the header is set from the\n")
	_, _ = fmt.Fprintf(sb, "// gateway-controlled context on EVERY exporter below, never from a literal\n")
	_, _ = fmt.Fprintf(sb, "// — see Config's doc comment. It does NOT guarantee the request reached\n")
	_, _ = fmt.Fprintf(sb, "// this pipeline via the gateway in the first place.\n")
	_, _ = fmt.Fprintf(sb, "otelcol.auth.headers %q {\n", label)
	_, _ = fmt.Fprintf(sb, "  header {\n")
	_, _ = fmt.Fprintf(sb, "    key          = %q\n", gateway.TenantHeader)
	_, _ = fmt.Fprintf(sb, "    from_context = %q\n", strings.ToLower(gateway.TenantHeader))
	_, _ = fmt.Fprintf(sb, "    action       = \"upsert\"\n")
	_, _ = fmt.Fprintf(sb, "  }\n")
	_, _ = fmt.Fprintf(sb, "}\n")
}

// renderBatch renders the batch processor between a receiver and its
// exporters. passThrough matters here for a reason that is easy to miss and
// silent when missed: the batch processor does NOT carry client metadata
// through to the exporters unless it is told which keys to preserve.
//
// Alloy's otelcol.auth.headers reference states it directly: "If an
// otelcol.processor.batch is present in the pipeline, it must be configured
// to preserve client metadata. Do this by adding the value that from_context
// needs to the metadata_keys of the batch processor."
//
// Without it, from_context finds nothing at the exporter and — per this
// package's own doc comment — omits the header rather than erroring. Every
// tenant's data then ships with NO tenant header: a loud outage against a
// strictly multi-tenant destination, and a silent cross-tenant merge against
// one with a default tenant. The config is syntactically valid either way,
// so `alloy validate` cannot catch it; this is docs/gateway-tier-plan.md
// §10's "alloy validate is not alloy run" one layer up.
func renderBatch(sb *strings.Builder, label string, batch BatchConfig, passThrough bool, metrics, logs, traces *OTLPExporter) {
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
	if passThrough {
		// Preserve exactly the one key the tenant auth component reads, and
		// nothing else: every distinct value of a preserved key creates its
		// own batch shard, so preserving more keys multiplies shards for no
		// benefit here.
		_, _ = fmt.Fprintf(sb, "  metadata_keys = [%q]\n", strings.ToLower(gateway.TenantHeader))
		// Batching is now per tenant, so the shard count is the number of
		// distinct tenants behind this listener. Stated explicitly rather
		// than left to the schema default (1000) because in pass-through
		// mode this number is a tenancy capacity limit, not a tuning knob —
		// a deployment past it starts dropping, and an operator reading the
		// generated config should see the bound they are living under.
		_, _ = fmt.Fprintf(sb, "  metadata_cardinality_limit = %d\n", passThroughTenantShardLimit)
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

// renderOTLPExporter renders one otelcol.exporter.otlp/otlphttp component.
// authRef, when non-empty, is an otelcol.auth.headers handler reference
// (e.g. "otelcol.auth.headers.otlp_shared_tenant.handler") rendered as the
// client block's auth attribute — set for every exporter in a
// TenancyPassThrough pipeline (see renderOTLPPipeline) and empty for every
// other caller (static OTLP exporters, and Faro's traces exporter, which has
// no pass-through mode).
func renderOTLPExporter(sb *strings.Builder, exp OTLPExporter, authRef string) {
	_, _ = fmt.Fprintf(sb, "%s %q {\n", otlpExporterComponent(exp.Protocol), exp.Name)
	_, _ = fmt.Fprintf(sb, "  client {\n")
	_, _ = fmt.Fprintf(sb, "    endpoint = %s\n", exp.EndpointExpr)
	if authRef != "" {
		_, _ = fmt.Fprintf(sb, "    auth = %s\n", authRef)
	}
	headerLines := mergedHeaderLines(exp.Headers, exp.SecretHeaderEnv)
	if len(headerLines) > 0 {
		_, _ = fmt.Fprintf(sb, "    headers = {\n")
		for _, line := range headerLines {
			_, _ = fmt.Fprintf(sb, "      %s\n", line)
		}
		_, _ = fmt.Fprintf(sb, "    }\n")
	}
	_, _ = fmt.Fprintf(sb, "  }\n")
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
		renderBatch(sb, faroTracesBatchLabel(p.Label), BatchConfig{}, false, nil, nil, p.Traces)
		sb.WriteString("\n")
		renderOTLPExporter(sb, *p.Traces, "") // Faro has no pass-through mode (D10)
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
