// Package receiver renders the receiver-tier Alloy pipeline: the config
// Shepherd serves to collectors with role=receiver (docs/gateway-tier-plan.md
// §3, W4). These collectors sit behind the tenant-aware Gateway API tier
// (internal/gateway) and receive telemetry directly from browsers (Grafana
// Faro RUM) and OTLP clients (SDKs, Lambdas, other collectors), then forward
// it to the tenant's destination (Mimir/Loki/Tempo/Pyroscope).
//
// Two pipeline shapes, matching internal/gateway.RouteKind:
//
//   - OTLP: otelcol.receiver.otlp (grpc and/or http) -> otelcol.processor.batch
//     -> otelcol.exporter.otlp/otlphttp, one per signal (metrics/logs/traces).
//   - Faro: faro.receiver (browser RUM) -> loki.write for logs,
//     otelcol.processor.batch -> otelcol exporter for traces.
//
// # CORS is this package's job, not the gateway's
//
// docs/gateway-tier-plan.md D2 records that at the pinned Gateway API floor
// (the v1.4 line), the Standard channel carries no CORS filter — it only
// reaches Standard in v1.6.1 — so Shepherd cannot depend on it without
// depending on an Experimental-channel feature, which D2 forbids. CORS for
// browser traffic is therefore answered entirely by faro.receiver's own
// cors_allowed_origins, rendered here. See CORSPolicy for how this package
// makes "which origins" a deliberate, explicit choice rather than an
// accidental default.
//
// # Rate and size limits
//
// Every limit the Alloy schema exposes on a receiver in this pipeline is
// rendered explicitly rather than left to fall out of Alloy's internal
// default (see the field docs on OTLPHTTPListener, OTLPGRPCListener, and
// FaroServer). Where the schema exposes no limit — otelcol.receiver.otlp has
// no requests/sec throttle, only size and concurrency caps — that gap is
// called out in a comment in the rendered output rather than left silent, so
// nobody reads the generated config and assumes a protection that is not
// there. Request-rate throttling for OTLP ingest has to live upstream, at the
// gateway or network layer.
package receiver
