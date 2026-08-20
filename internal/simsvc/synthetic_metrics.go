package simsvc

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"shepherd/internal/simulate"
)

// SyntheticExporter is the target every stubbed discovery node scrapes
// (§6.4 step 2: "counters advancing, a gauge, histogram — enough to exercise
// scrape+relabel+write for real").
//
// Every series moves on Advance() rather than on wall-clock time, so a scrape
// taken after N advances is the same in a test as in a run. A time-driven
// exporter would make "did the counter advance?" a flaky question.
type SyntheticExporter struct {
	registry  *prometheus.Registry
	requests  *prometheus.CounterVec
	queue     prometheus.Gauge
	durations prometheus.Histogram
	tick      int
}

// Label values the synthetic request counter is broken down by. Two paths and
// two methods give four series — enough for a relabel chain to actually keep
// and drop different things, which is the point of scraping a synthetic target
// at all.
var syntheticRequestSeries = []struct{ path, method string }{
	{"/api/health", "GET"},
	{"/api/pipelines", "GET"},
	{"/api/pipelines", "POST"},
	{"/api/collectors", "GET"},
}

// NewSyntheticExporter builds the exporter on a private registry: the Go and
// process collectors are deliberately absent so a captured series table shows
// only series the user's pipeline actually asked for.
func NewSyntheticExporter() *SyntheticExporter {
	e := &SyntheticExporter{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "shepherd_sim_requests_total",
			Help: "Synthetic request counter served by the sandbox harness.",
		}, []string{"path", "method"}),
		queue: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "shepherd_sim_queue_depth",
			Help: "Synthetic queue depth served by the sandbox harness.",
		}),
		durations: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "shepherd_sim_request_duration_seconds",
			Help:    "Synthetic request duration served by the sandbox harness.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}),
	}
	e.registry.MustRegister(e.requests, e.queue, e.durations)
	// Register every counter series at zero up front so the first scrape
	// already carries all four; a counter that appears mid-run looks to
	// Prometheus like a reset.
	for _, s := range syntheticRequestSeries {
		e.requests.WithLabelValues(s.path, s.method)
	}
	e.Advance()
	return e
}

// Advance moves every series by a fixed step. Called on a ticker during a run.
func (e *SyntheticExporter) Advance() {
	e.tick++
	for i, s := range syntheticRequestSeries {
		// A different step per series so rate() differs between them and a
		// relabel chain that keeps only some has a visibly different result.
		e.requests.WithLabelValues(s.path, s.method).Add(float64(i + 1))
	}
	// Deterministic sawtooth: a gauge that only rises is indistinguishable
	// from a counter downstream.
	e.queue.Set(float64(e.tick % 8))
	e.durations.Observe(float64((e.tick%10)+1) / 100)
}

// Handler serves simulate.SyntheticMetricsPath. The stub targets carry no
// __metrics_path__, so Prometheus's default path applies and this mux must
// serve exactly it.
func (e *SyntheticExporter) Handler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(simulate.SyntheticMetricsPath, promhttp.HandlerFor(e.registry, promhttp.HandlerOpts{}))
	return mux
}
