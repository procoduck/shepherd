package simsvc

import (
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"

	"shepherd/internal/simulate"
)

// Harness owns every capture receiver and the sink the active run accumulates
// into.
//
// There is exactly ONE active sink at a time, and the queue enforces one
// running sandbox, because the capture URLs the transform writes carry no run
// discriminator: two concurrent runs would post to the same paths and their
// series would silently merge. Handing a user another user's series is worse
// than making them wait, so concurrency is the thing that gives.
type Harness struct {
	logger *slog.Logger

	mu     sync.RWMutex
	active *sink
}

// NewHarness builds a harness with no active run; captures arriving before the
// first run starts are discarded.
func NewHarness(logger *slog.Logger) *Harness {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Harness{logger: logger}
}

// begin makes a fresh sink active and returns it. A previously active sink is
// detached, so late arrivals from the previous run cannot contaminate this one.
func (h *Harness) begin() *sink {
	s := newSink()
	h.mu.Lock()
	h.active = s
	h.mu.Unlock()
	return s
}

// end detaches the active sink if it is still the one passed in.
func (h *Harness) end(s *sink) {
	h.mu.Lock()
	if h.active == s {
		h.active = nil
	}
	h.mu.Unlock()
}

func (h *Harness) activeSink() *sink {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active
}

func (h *Harness) recordOTLP(sig otlpSignal) {
	if s := h.activeSink(); s != nil {
		s.addOTLP(sig.metricPoints, sig.logRecords, sig.spans, sig.attrs)
	}
}

// CaptureRoutes returns every path the capture mux serves, sorted. It exists
// so a test can assert the harness routes exactly the paths the transform
// writes — the two halves of the contract in internal/simulate/harness_paths.go
// meeting in an assertion rather than in production.
func CaptureRoutes() []string {
	routes := []string{
		simulate.CapturePathPrometheus,
		simulate.CapturePathLoki,
		simulate.CapturePathPyroscope,
		simulate.CapturePathFaro,
		simulate.CapturePathSplunkHEC,
		simulate.CapturePrefixOTLPHTTP + simulate.OTLPSuffixMetrics,
		simulate.CapturePrefixOTLPHTTP + simulate.OTLPSuffixLogs,
		simulate.CapturePrefixOTLPHTTP + simulate.OTLPSuffixTraces,
	}
	sort.Strings(routes)
	return routes
}

// CaptureMux builds the HTTP capture receiver mux.
func (h *Harness) CaptureMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+simulate.CapturePathPrometheus, h.handlePrometheusWrite)
	mux.HandleFunc("POST "+simulate.CapturePathLoki, h.handleLokiPush)

	metricsResp, logsResp, tracesResp := otlpEmptyResponses()
	mux.Handle("POST "+simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixMetrics, h.otlpHTTPHandler(decodeOTLPMetrics, metricsResp))
	mux.Handle("POST "+simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixLogs, h.otlpHTTPHandler(decodeOTLPLogs, logsResp))
	mux.Handle("POST "+simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixTraces, h.otlpHTTPHandler(decodeOTLPTraces, tracesResp))

	// Pyroscope, Faro and Splunk HEC bodies are counted rather than decoded:
	// the results view claims only "the pipeline delivered" for them, and a
	// full decode of three more wire formats would be three more schemas to
	// keep in step with an Alloy bump for no extra signal.
	mux.HandleFunc("POST "+simulate.CapturePathPyroscope, h.countingHandler(func(o *OtherCounts) { o.PyroscopeRequests++ }))
	mux.HandleFunc("POST "+simulate.CapturePathFaro, h.countingHandler(func(o *OtherCounts) { o.FaroRequests++ }))
	mux.HandleFunc("POST "+simulate.CapturePathSplunkHEC, h.countingHandler(func(o *OtherCounts) { o.SplunkHECRequests++ }))
	return mux
}

// countingHandler drains and counts a request body. Draining matters: an
// exporter whose body is not read sees a broken pipe and reports an export
// failure the user would read as a pipeline fault.
func (h *Harness) countingHandler(mutate func(*OtherCounts)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, maxCaptureBody)); err != nil {
			h.logger.Warn("capture: drain body failed", "path", r.URL.Path, "error", err)
		}
		if s := h.activeSink(); s != nil {
			s.addOther(mutate)
		}
		w.WriteHeader(http.StatusOK)
	}
}
