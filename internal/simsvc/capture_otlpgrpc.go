package simsvc

import (
	"context"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// The gRPC OTLP receiver exists because the overlay maps
// otelcol.exporter.otlp to receiver "otlp_grpc" and the transform hands it a
// bare host:port. Rewriting that exporter to its HTTP sibling instead would
// change the protocol under the user and make the sandbox report on a pipeline
// they did not author — the fidelity lie §6.5 refuses.
//
// All three OTLP collector services declare a method named Export with
// different signatures, so each needs its own Go type.
type metricsExporter struct {
	collectormetrics.UnimplementedMetricsServiceServer
	harness *Harness
}

type logsExporter struct {
	collectorlogs.UnimplementedLogsServiceServer
	harness *Harness
}

type traceExporter struct {
	collectortrace.UnimplementedTraceServiceServer
	harness *Harness
}

func registerOTLPGRPC(s grpc.ServiceRegistrar, h *Harness) {
	collectormetrics.RegisterMetricsServiceServer(s, &metricsExporter{harness: h})
	collectorlogs.RegisterLogsServiceServer(s, &logsExporter{harness: h})
	collectortrace.RegisterTraceServiceServer(s, &traceExporter{harness: h})
}

func (e *metricsExporter) Export(_ context.Context, req *collectormetrics.ExportMetricsServiceRequest) (*collectormetrics.ExportMetricsServiceResponse, error) {
	sig := otlpSignal{}
	for _, rm := range req.GetResourceMetrics() {
		sig.attrs = append(sig.attrs, attrNames(rm.GetResource().GetAttributes())...)
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				sig.metricPoints += metricPointCount(m)
			}
		}
	}
	e.harness.recordOTLP(sig)
	return &collectormetrics.ExportMetricsServiceResponse{}, nil
}

func (e *logsExporter) Export(_ context.Context, req *collectorlogs.ExportLogsServiceRequest) (*collectorlogs.ExportLogsServiceResponse, error) {
	sig := otlpSignal{}
	for _, rl := range req.GetResourceLogs() {
		sig.attrs = append(sig.attrs, attrNames(rl.GetResource().GetAttributes())...)
		for _, sl := range rl.GetScopeLogs() {
			sig.logRecords += len(sl.GetLogRecords())
		}
	}
	e.harness.recordOTLP(sig)
	return &collectorlogs.ExportLogsServiceResponse{}, nil
}

func (e *traceExporter) Export(_ context.Context, req *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	sig := otlpSignal{}
	for _, rs := range req.GetResourceSpans() {
		sig.attrs = append(sig.attrs, attrNames(rs.GetResource().GetAttributes())...)
		for _, ss := range rs.GetScopeSpans() {
			sig.spans += len(ss.GetSpans())
		}
	}
	e.harness.recordOTLP(sig)
	return &collectortrace.ExportTraceServiceResponse{}, nil
}
