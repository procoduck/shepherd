package simsvc

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// readOTLPBody returns the decompressed request body. The gunzip branch is not
// optional: measured against real Alloy v1.18.1, otelcol.exporter.otlphttp
// gzips by default, and a plain proto handler answers 400 — at which point
// Alloy logs "Exporting failed. Dropping data" and the run reports an empty
// capture that looks exactly like a broken pipeline.
func readOTLPBody(header http.Header, body io.Reader) ([]byte, error) {
	limited := io.LimitReader(body, maxCaptureBody)
	if strings.EqualFold(strings.TrimSpace(header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(limited)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer zr.Close() //nolint:errcheck // read-only reader; close error is not actionable
		raw, err := io.ReadAll(io.LimitReader(zr, maxCaptureBody))
		if err != nil {
			return nil, fmt.Errorf("gunzip body: %w", err)
		}
		return raw, nil
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return raw, nil
}

// otlpSignal is what one decoded OTLP export contributed.
type otlpSignal struct {
	metricPoints int
	logRecords   int
	spans        int
	attrs        []string
}

func decodeOTLPMetrics(raw []byte) (otlpSignal, error) {
	var req collectormetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return otlpSignal{}, fmt.Errorf("unmarshal otlp metrics: %w", err)
	}
	out := otlpSignal{}
	for _, rm := range req.GetResourceMetrics() {
		out.attrs = append(out.attrs, attrNames(rm.GetResource().GetAttributes())...)
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				out.metricPoints += metricPointCount(m)
			}
		}
	}
	return out, nil
}

func decodeOTLPLogs(raw []byte) (otlpSignal, error) {
	var req collectorlogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return otlpSignal{}, fmt.Errorf("unmarshal otlp logs: %w", err)
	}
	out := otlpSignal{}
	for _, rl := range req.GetResourceLogs() {
		out.attrs = append(out.attrs, attrNames(rl.GetResource().GetAttributes())...)
		for _, sl := range rl.GetScopeLogs() {
			out.logRecords += len(sl.GetLogRecords())
		}
	}
	return out, nil
}

func decodeOTLPTraces(raw []byte) (otlpSignal, error) {
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return otlpSignal{}, fmt.Errorf("unmarshal otlp traces: %w", err)
	}
	out := otlpSignal{}
	for _, rs := range req.GetResourceSpans() {
		out.attrs = append(out.attrs, attrNames(rs.GetResource().GetAttributes())...)
		for _, ss := range rs.GetScopeSpans() {
			out.spans += len(ss.GetSpans())
		}
	}
	return out, nil
}

func metricPointCount(m *metricspb.Metric) int {
	switch d := m.GetData().(type) {
	case *metricspb.Metric_Gauge:
		return len(d.Gauge.GetDataPoints())
	case *metricspb.Metric_Sum:
		return len(d.Sum.GetDataPoints())
	case *metricspb.Metric_Histogram:
		return len(d.Histogram.GetDataPoints())
	case *metricspb.Metric_ExponentialHistogram:
		return len(d.ExponentialHistogram.GetDataPoints())
	case *metricspb.Metric_Summary:
		return len(d.Summary.GetDataPoints())
	}
	return 0
}

func attrNames(attrs []*commonpb.KeyValue) []string {
	out := make([]string, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, a.GetKey())
	}
	return out
}

// otlpHTTPHandler builds the handler for one OTLP/HTTP signal path. The reply
// must be a 200 carrying an empty Export*ServiceResponse in
// application/x-protobuf: anything else and the exporter treats the export as
// failed and drops the batch.
func (h *Harness) otlpHTTPHandler(decode func([]byte) (otlpSignal, error), response proto.Message) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := readOTLPBody(r.Header, r.Body)
		if err != nil {
			h.logger.Warn("capture: otlp body read failed", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sig, err := decode(raw)
		if err != nil {
			h.logger.Warn("capture: otlp decode failed", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s := h.activeSink(); s != nil {
			s.addOTLP(sig.metricPoints, sig.logRecords, sig.spans, sig.attrs)
		}
		body, err := proto.Marshal(response)
		if err != nil {
			h.logger.Error("capture: marshal otlp response", "error", err)
			http.Error(w, "marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			h.logger.Warn("capture: write otlp response", "error", err)
		}
	}
}

// otlpEmptyResponses returns the three empty success responses, one per
// signal.
func otlpEmptyResponses() (metrics, logs, traces proto.Message) {
	return &collectormetrics.ExportMetricsServiceResponse{},
		&collectorlogs.ExportLogsServiceResponse{},
		&collectortrace.ExportTraceServiceResponse{}
}
