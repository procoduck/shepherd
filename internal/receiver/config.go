package receiver

// Config is everything needed to render one receiver-tier Alloy pipeline. It
// may hold any number of OTLP and Faro pipelines. docs/gateway-tier-plan.md
// §11 leaves "one Alloy per tenant vs. one shared receiver with per-tenant
// pipelines" as an open decision (item 4); this package does not resolve it
// — a Config with a single entry renders the former, a Config with several
// entries (each with distinct listener addresses/ports) renders the latter.
// Either way, each pipeline entry is scoped to exactly one tenant: its
// exporters carry that tenant's destination and identity statically, decided
// at render time by the caller, not extracted from the request at runtime.
type Config struct {
	// OTLP is the set of OTLP receiver pipelines to render, in order.
	OTLP []OTLPPipeline
	// Faro is the set of Faro (browser RUM) receiver pipelines to render, in
	// order.
	Faro []FaroPipeline
}

// ExporterProtocol selects which OTLP exporter component a signal is sent
// through.
type ExporterProtocol string

const (
	// ExporterGRPC renders otelcol.exporter.otlp (OTLP/gRPC).
	ExporterGRPC ExporterProtocol = "grpc"
	// ExporterHTTP renders otelcol.exporter.otlphttp (OTLP/HTTP).
	ExporterHTTP ExporterProtocol = "http"
)

// OTLPPipeline is one otelcol.receiver.otlp -> otelcol.processor.batch ->
// exporter(s) chain, all sharing the Label as a suffix.
type OTLPPipeline struct {
	// Label is the Alloy component label for every component this pipeline
	// renders (e.g. otelcol.receiver.otlp "<Label>"). Must be a valid Alloy
	// identifier and unique among every OTLP and Faro pipeline in the Config.
	Label string
	// HTTP configures the http block of otelcol.receiver.otlp. Nil disables
	// the HTTP listener. At least one of HTTP/GRPC must be set.
	HTTP *OTLPHTTPListener
	// GRPC configures the grpc block of otelcol.receiver.otlp. Nil disables
	// the gRPC listener. At least one of HTTP/GRPC must be set.
	GRPC *OTLPGRPCListener
	// Batch configures the otelcol.processor.batch sitting between the
	// receiver and the exporters. The zero value is valid: every field left
	// unset means "let Alloy apply its own component default" for that
	// field, and none of those defaults are limits this package's contract
	// promises to make explicit (batching cadence is a throughput knob, not
	// a safety control).
	Batch BatchConfig
	// Metrics, Logs, Traces are the per-signal destinations. A nil field
	// means that signal is not forwarded (its otelcol.receiver.otlp output
	// attribute is left unset). At least one must be set.
	Metrics *OTLPExporter
	Logs    *OTLPExporter
	Traces  *OTLPExporter
}

// OTLPHTTPListener configures otelcol.receiver.otlp's http block. Every
// field here exists in the pinned schema (internal/schema/artifacts) and is
// required by this package precisely because it is the field that expresses
// a size limit — see doc.go.
type OTLPHTTPListener struct {
	// ListenAddr is the http block's endpoint attribute, e.g. "0.0.0.0:4318".
	// Required — no implicit default is inherited from Alloy.
	ListenAddr string
	// MaxRequestBodySize is the http block's max_request_body_size, e.g.
	// "8MiB". Required: the schema exposes no default for this field (unlike
	// e.g. read_buffer_size), so an unset value here would silently inherit
	// whatever the compiled-in collector default happens to be rather than a
	// value this pipeline's author actually chose.
	MaxRequestBodySize string
}

// OTLPGRPCListener configures otelcol.receiver.otlp's grpc block.
type OTLPGRPCListener struct {
	// ListenAddr is the grpc block's endpoint attribute, e.g. "0.0.0.0:4317".
	// Required.
	ListenAddr string
	// MaxRecvMsgSize is the grpc block's max_recv_msg_size, e.g. "4MiB".
	// Required, for the same reason as OTLPHTTPListener.MaxRequestBodySize:
	// the schema exposes no default.
	MaxRecvMsgSize string
	// MaxConcurrentStreams is the grpc block's max_concurrent_streams: a
	// concurrency cap, not a rate limit, but the closest thing
	// otelcol.receiver.otlp's gRPC listener has to one. Required (>0) so a
	// caller has to decide it rather than inherit gRPC's unbounded default.
	MaxConcurrentStreams int
}

// BatchConfig configures otelcol.processor.batch. Zero values are omitted
// from the render, letting Alloy apply its own default for that field.
type BatchConfig struct {
	// Timeout is the batch block's timeout duration, e.g. "5s".
	Timeout string
	// SendBatchSize is send_batch_size.
	SendBatchSize int
	// SendBatchMaxSize is send_batch_max_size.
	SendBatchMaxSize int
}

// OTLPExporter configures one otelcol.exporter.otlp/otlphttp destination for
// a single signal.
type OTLPExporter struct {
	// Name is the Alloy component label, unique among every OTLP exporter in
	// the Config (both otelcol.exporter.otlp and otelcol.exporter.otlphttp
	// share one Alloy namespace per component type, but this package also
	// requires uniqueness across protocols to keep generated labels
	// unambiguous to a human reading the file).
	Name string
	// Protocol selects otelcol.exporter.otlp (ExporterGRPC) or
	// otelcol.exporter.otlphttp (ExporterHTTP).
	Protocol ExporterProtocol
	// EndpointExpr is a raw Alloy expression evaluating to the destination
	// URL, e.g. `sys.env("SHEPHERD_DEST_ACME_MIMIR_URL")` or a quoted
	// literal `"https://mimir.example.com:9095"`. Required. This package
	// never decides how a destination URL is resolved — the caller supplies
	// the exact Alloy expression, matching the convention already
	// established in internal/wizard/appobservability/wizard.go ("auth
	// injected by Shepherd at serve time").
	EndpointExpr string
	// Headers sets literal (non-secret) entries in the exporter client's
	// headers map, e.g. the tenant identifier. Values are rendered as quoted
	// string literals — never put a secret here.
	Headers map[string]string
	// SecretHeaderEnv sets entries in the exporter client's headers map whose
	// value must never be a literal: each value is an environment variable
	// name, rendered as `sys.env("<value>")`. Use this for bearer tokens or
	// any other credential.
	SecretHeaderEnv map[string]string
}

// FaroPipeline is one faro.receiver -> destinations chain.
type FaroPipeline struct {
	// Label is the Alloy component label, unique among every OTLP and Faro
	// pipeline in the Config.
	Label string
	// Server configures faro.receiver's server block.
	Server FaroServer
	// CORS decides which browser origins may read this receiver's response.
	// See CORSPolicy — the zero value is CORS disabled, which is also
	// Alloy's own default for an empty cors_allowed_origins list.
	CORS CORSPolicy
	// Logs is the destination for the loki.logs side of faro.receiver's
	// output (rendered as loki.write). Nil means logs are not forwarded.
	Logs *LokiExporter
	// Traces is the destination for the otel.traces side of faro.receiver's
	// output (rendered through otelcol.processor.batch into an OTLP
	// exporter). Nil means traces are not forwarded. At least one of
	// Logs/Traces must be set.
	Traces *OTLPExporter
}

// FaroServer configures faro.receiver's server block.
type FaroServer struct {
	// ListenAddr is the server block's listen_address, e.g. "0.0.0.0".
	// Required.
	ListenAddr string
	// ListenPort is the server block's listen_port. Required (>0).
	ListenPort int
	// MaxAllowedPayloadSize is max_allowed_payload_size, e.g. "5MiB".
	// Required: Alloy's own default (5MiB) is a real, sane value, but this
	// package requires it be named explicitly rather than silently inherited
	// — the same "deliberate, not accidental" bar CORS is held to.
	MaxAllowedPayloadSize string
	// RateLimit configures the server's rate_limiting block, the one place
	// this receiver-tier pipeline can express a requests/sec cap at all.
	RateLimit RateLimit
}

// RateLimit configures faro.receiver's server.rate_limiting block.
type RateLimit struct {
	// Enabled toggles the block's enabled attribute. Required to be set
	// explicitly true or false by a caller that has thought about it — see
	// Validate.
	Enabled bool
	// Strategy is "global" or "per_app". Required when Enabled.
	Strategy string
	// Rate is requests/sec. Required (>0) when Enabled.
	Rate int
	// BurstSize is the burst allowance. Required (>0) when Enabled.
	BurstSize int
}

// LokiExporter configures a loki.write destination for Faro's log output.
// Unlike OTLPExporter, loki.write exposes tenant scoping and secret auth as
// first-class typed attributes (tenant_id, bearer_token), so this type does
// not need the generic Headers/SecretHeaderEnv maps.
type LokiExporter struct {
	// Name is the Alloy component label, unique among every Loki exporter in
	// the Config.
	Name string
	// URLExpr is a raw Alloy expression for the endpoint URL, same
	// convention as OTLPExporter.EndpointExpr. Required.
	URLExpr string
	// TenantID is rendered as the endpoint block's tenant_id attribute, a
	// literal string. Not a secret — it is the same tenant identifier the
	// gateway already injects downstream (internal/gateway.TenantHeader).
	TenantID string
	// BearerTokenEnv, if set, is an environment variable name rendered as
	// `bearer_token = sys.env("<value>")`. Never a literal.
	BearerTokenEnv string
}

// CORSPolicy decides which browser origins faro.receiver answers with CORS
// headers, per docs/gateway-tier-plan.md D2. The zero value renders
// cors_allowed_origins = [] — CORS disabled, matching Alloy's own default
// (empty list means the browser cannot read a cross-origin response). That
// makes "no origins configured" a safe default at the Go zero-value level,
// not just a runtime check: a caller has to explicitly populate Origins or
// set AllowAll to get anything else.
type CORSPolicy struct {
	// Origins is the explicit allowlist, e.g.
	// []string{"https://app.example.com"}. A literal "*" entry is refused by
	// Validate — wildcard is only ever reached through AllowAll, so "accept
	// every origin" is a named, auditable choice and never something that
	// falls out of a normal-looking origin string.
	Origins []string
	// AllowAll, when true, renders cors_allowed_origins = ["*"] regardless of
	// Origins. Validate refuses AllowAll=true combined with a non-empty
	// Origins, so the two are never ambiguous about which one is meant.
	AllowAll bool
}
