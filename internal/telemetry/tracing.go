package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"shepherd/internal/config"
)

// ShutdownFunc flushes and stops the tracer provider. Always non-nil, so the
// caller can defer it without a nil check.
type ShutdownFunc func(context.Context) error

// InitTracing configures the global OpenTelemetry tracer provider.
//
// Tracing is OFF unless tracing.endpoint is set, and off means off: no
// provider is installed, no exporter goroutine starts, and otel's own no-op
// tracer makes every Start call in this package free. A deployment that does
// not want traces pays nothing for the instrumentation being present, which is
// the only way instrumenting the hot agent path is defensible.
//
// The returned ShutdownFunc must be called on exit — spans are batched, so
// skipping it drops whatever is still buffered, which is exactly the tail of a
// crash you most wanted to see.
func InitTracing(ctx context.Context, cfg *config.Config, logger *slog.Logger) (ShutdownFunc, error) {
	noop := func(context.Context) error { return nil }
	if cfg.Tracing.Endpoint == "" {
		return noop, nil
	}

	exporter, err := newExporter(ctx, cfg.Tracing)
	if err != nil {
		return noop, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	// NewSchemaless, not NewWithAttributes(semconv.SchemaURL, ...):
	// resource.Merge refuses to merge two resources that declare DIFFERENT
	// schema URLs, and resource.Default() carries whichever version the SDK
	// was built against. Pinning our own semconv version here made that a
	// version-coupling landmine — it failed with "conflicting Schema URL"
	// the first time this ran against a real collector, and because the error
	// is non-fatal it disabled tracing while looking like it had started.
	// A schemaless resource merges cleanly with any SDK version; the
	// attribute keys are still the semantic-convention ones.
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(cfg.Tracing.ServiceName),
		attribute.String("service.version", cfg.Tracing.ServiceVersion),
	))
	if err != nil {
		return noop, fmt.Errorf("building trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// ParentBased so a caller's sampling decision is honoured: sampling an
		// incoming request's children independently produces broken traces
		// where the root is missing.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Tracing.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	logger.Info("tracing enabled",
		"endpoint", cfg.Tracing.Endpoint,
		"protocol", cfg.Tracing.Protocol,
		"sample_ratio", cfg.Tracing.SampleRatio,
		"service_name", cfg.Tracing.ServiceName)

	return func(shutdownCtx context.Context) error {
		// Bounded: a dead collector must not hold up process exit.
		shutdownCtx, cancel := context.WithTimeout(shutdownCtx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(shutdownCtx)
	}, nil
}

// newExporter builds the OTLP exporter for the configured protocol. Both
// transports are supported because collectors differ on which they expose —
// the OTel Collector's default receiver takes gRPC on 4317 and HTTP on 4318,
// and picking one for the operator would just mean they cannot use the other.
func newExporter(ctx context.Context, cfg config.TracingConfig) (sdktrace.SpanExporter, error) {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "http://"), "https://")

	switch strings.ToLower(cfg.Protocol) {
	case "grpc", "":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracegrpc.WithInsecure())
		}
		return otlptracegrpc.New(ctx, opts...)
	case "http", "http/protobuf":
		opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		return otlptracehttp.New(ctx, opts...)
	default:
		return nil, fmt.Errorf("unsupported tracing protocol %q (want \"grpc\" or \"http\")", cfg.Protocol)
	}
}
