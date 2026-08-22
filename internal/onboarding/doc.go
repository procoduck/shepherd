// Package onboarding renders the "connect an app" artifacts W7 exists to
// produce (docs/gateway-tier-plan.md §4): given a tenant's OTLP route, emit
// the endpoint, environment variables, and IaC/SDK snippets a service owner
// pastes in to start shipping telemetry — Lambda env vars, Terraform/SAM/CDK,
// container/Kubernetes env, and SDK-init notes. No proto change, no database
// migration: every input here is a value the caller already holds (a
// gateway.RouteSpec and a display name), and every output is text.
//
// # OTLP only, and specifically OTLP/HTTP (not gRPC)
//
// D5 (docs/gateway-tier-plan.md) already rules out an OpenTelemetry
// Collector config renderer in favor of "an endpoint plus onboarding
// artifacts" — this package is that other half. D10 goes further and makes
// OTLP the first-class frontend path; Faro (browser RUM) is demand-driven
// and does not exist yet, so this package renders no Faro snippet — ConnectAppSpec.Route.Kind
// must be gateway.KindOTLP, and Render refuses anything else. D10's
// documented hybrid (Faro SDK for RUM batteries, its own OTLP exporter for
// traces, pointed at THIS package's endpoint) is worth a paragraph in
// RenderSDKNotes, not a rendered Faro snippet — there is nothing to render
// until faro.receiver support ships.
//
// This package goes one step further than D10 states explicitly and refuses
// OTLP/gRPC too, for a reason specific to how tenant routing works (D9): the
// gateway identifies a tenant purely from the URL PATH prefix
// ("/otlp/<segment>/..." — internal/gateway.RouteSpec.PathPrefix), which
// internal/gateway.RenderHTTPRoute strips with a URLRewrite filter before the
// request reaches the receiver tier. That scheme only exists for protocols
// where the client puts a meaningful, configurable string in the request
// path. OTLP/HTTP does: an exporter is given a base URL and appends
// "v1/traces" etc. to whatever path that base URL already has (verified
// against the OpenTelemetry specification's OTLP exporter configuration doc,
// 2026-08-22 — see BaseEndpoint/SignalEndpoint). OTLP/gRPC does not: a gRPC
// client's :path pseudo-header is fixed by the generated stub
// ("/opentelemetry.proto.collector.trace.v1.TraceService/Export" and
// friends) — nothing in OTEL_EXPORTER_OTLP_ENDPOINT or any standard SDK
// config can prepend the tenant segment onto it. A gRPC-configured client
// would dial the gateway's host and send that fixed path, which no
// HTTPRoute this package's endpoints describe ever matches — the exact
// "endpoint that no route serves" failure plan §5 forbids, except no golden
// text diff would ever show it, because the endpoint STRING looks identical
// for both protocols; only the wire behavior differs. So Protocol is typed
// to the two OTLP/HTTP wire formats only (ProtocolHTTPProtobuf,
// ProtocolHTTPJSON) and Validate names this exact reason if asked for
// anything else. Subdomain-per-tenant routing (D9, "a documented future
// variant, not built") would remove the path dependency and make gRPC
// viable; until it exists, gRPC has no correct rendering this package can
// produce, so it renders none.
//
// # The compatibility boundary this package exists to get right
//
// docs/gateway-tier-plan.md's D9 compatibility note: OTLP/HTTP SDKs append
// "v1/traces"/"v1/metrics"/"v1/logs" to OTEL_EXPORTER_OTLP_ENDPOINT
// themselves, so a client only ever needs the BASE endpoint
// (BaseEndpoint) — appending a signal path here would double it. The
// per-signal variables (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and friends), by
// contrast, are specified to be used "as-is without any modification" — a
// tool that sets one of those MUST be given the full signal path
// (SignalEndpoint), or traffic lands on the gateway's bare tenant prefix,
// which no receiver-tier route serves. Getting this backwards in either
// direction is a silent 404, never a compile error, which is why
// endpoint.go carries the citation and endpoint_test.go proves both forms
// against the gateway package's own renderer rather than a restated format
// string.
//
// # No hardcoded ADOT ARNs (D5's corollary)
//
// AWS's OpenTelemetry Lambda layer ARNs are region- and architecture-
// specific and are not pinned/freshness-checked the way ALLOY_VERSION is —
// D5's corollary forbids hardcoding one. Every Lambda-facing artifact this
// package renders (render_lambda.go, render_terraform.go, render_sam.go,
// render_cdk.go) takes the layer ARN as a caller-supplied parameter/variable
// and links AWS's own published table
// (https://aws-otel.github.io/docs/getting-started/lambda, confirmed
// 2026-08-22 to be the region/runtime ARN table) instead of filling one in.
package onboarding
