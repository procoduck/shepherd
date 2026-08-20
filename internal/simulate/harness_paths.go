package simulate

// The HTTP paths the S3 capture harness serves, hung off
// HarnessEndpoints.CaptureBaseURL. They live here rather than as literals in
// receiverEndpoint because the transform (which writes them into a user's
// config) and the harness in internal/simsvc (which routes them) are the two
// halves of the same contract and have no other way to meet: a path typo on
// either side produces a run that is green everywhere and captures nothing,
// which §6.5 calls out as the one lie simulation must never tell.
//
// Every constant is a full path with a leading slash and no trailing slash.
const (
	// CapturePathPrometheus receives snappy-framed prompb.WriteRequest bodies
	// from prometheus.remote_write.
	CapturePathPrometheus = "/capture/prometheus/api/v1/write"
	// CapturePathLoki receives snappy-framed push.PushRequest bodies from
	// loki.write. The doubled segment is deliberate: loki.write appends the
	// well-known /loki/api/v1/push suffix itself only when the configured URL
	// has no path, so the transform writes the full path and the harness must
	// serve exactly it.
	CapturePathLoki = "/capture/loki/loki/api/v1/push"
	// CapturePathPyroscope receives pyroscope.write ingest bodies.
	CapturePathPyroscope = "/capture/pyroscope/ingest"
	// CapturePrefixOTLPHTTP is the BASE the otelcol OTLP/HTTP client is pointed
	// at; it appends /v1/metrics, /v1/logs and /v1/traces itself.
	CapturePrefixOTLPHTTP = "/capture/otlphttp"
	// CapturePathFaro receives otelcol.exporter.faro payloads.
	CapturePathFaro = "/capture/faro"
	// CapturePathSplunkHEC receives otelcol.exporter.splunkhec payloads.
	CapturePathSplunkHEC = "/capture/splunkhec/services/collector"
)

// OTLP signal suffixes the OTLP/HTTP client appends to CapturePrefixOTLPHTTP.
const (
	OTLPSuffixMetrics = "/v1/metrics"
	OTLPSuffixLogs    = "/v1/logs"
	OTLPSuffixTraces  = "/v1/traces"
)

// SyntheticMetricsPath is the path the synthetic exporter serves. The stub
// targets carry no __metrics_path__ label, so Prometheus's default applies and
// the harness must serve that exact path.
const SyntheticMetricsPath = "/metrics"

// StubLogFileName returns the file the synthetic log emitter writes for a
// fixture, relative to HarnessEndpoints.LogDir. The loki_file stubs tail
// exactly this name.
func StubLogFileName(fixture string) string { return fixture + ".log" }
