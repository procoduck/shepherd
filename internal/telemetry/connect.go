package telemetry

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"shepherd/internal/metrics"
)

// tracerName identifies spans this package produces.
const tracerName = "shepherd/internal/telemetry"

// RequestGate wraps a connect.RequestGateFunc so a call the gate refuses is
// still counted in RPCRequestsTotal under its Connect code.
//
// A gate runs before the request body is read and before the interceptor
// chain, which is the point of using one for authentication — an
// unauthenticated caller costs a header check, not a decompress and a
// decode. The price is that Interceptor never sees the refused call, and an
// authentication-failure spike is exactly the thing you want on a graph.
// Counting here keeps the metric complete; no duration is observed for a
// refused call, because there is no handler work to time.
func RequestGate(gate connect.RequestGateFunc) connect.RequestGateFunc {
	return func(ctx context.Context, spec connect.Spec, peer connect.Peer, header http.Header) (context.Context, error) {
		ctx, err := gate(ctx, spec, peer, header)
		if err != nil {
			metrics.RPCRequestsTotal.WithLabelValues(spec.Procedure, connect.CodeOf(err).String()).Inc()
		}
		return ctx, err
	}
}

// Interceptor returns a Connect interceptor that records RPC metrics and, when
// tracing is enabled, a server span per call.
//
// Written here rather than pulled from connectrpc.com/otelconnect because the
// job is small and the dependency is not: metrics and a span over one unary
// call is the whole of it, and this way the metric names line up with the
// HTTP middleware's instead of following another library's conventions.
//
// Both the agent-facing collector.v1 handlers and the management
// shepherd.mgmt.v1 handlers mount this, so "which RPC is slow" has one answer
// across both surfaces.
func Interceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			// Continue an inbound trace when the caller propagated one. Without
			// this every RPC starts a new root span and a request that crossed
			// from the UI into the API looks like two unrelated traces.
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(req.Header()))
			ctx, span := otel.Tracer(tracerName).Start(ctx, procedure,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(semconv.RPCSystemConnectRPC, semconv.RPCMethod(procedure)),
			)
			defer span.End()

			start := time.Now()
			resp, err := next(ctx, req)

			// connect.CodeOf reports CodeUnknown for a nil error, so the
			// success case is labelled explicitly rather than by its code.
			code := "ok"
			if err != nil {
				code = connect.CodeOf(err).String()
				span.SetStatus(codes.Error, err.Error())
			}
			span.SetAttributes(attribute.String("rpc.connect.status_code", code))

			metrics.RPCRequestsTotal.WithLabelValues(procedure, code).Inc()
			metrics.RPCDuration.WithLabelValues(procedure).Observe(time.Since(start).Seconds())
			return resp, err
		}
	}
}
