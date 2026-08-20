package simsvc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"shepherd/internal/simulate"
)

// scrapeSynthetic scrapes the exporter the same way the sandbox's
// prometheus.scrape does — over HTTP, on the default metrics path — and parses
// the exposition format. Reading the registry directly would not prove the
// path is served.
func scrapeSynthetic(e *SyntheticExporter) map[string]float64 {
	GinkgoHelper()
	req := httptest.NewRequest(http.MethodGet, simulate.SyntheticMetricsPath, nil)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, req)
	Expect(rec.Code).To(Equal(http.StatusOK))

	// The parser needs an explicit validation scheme: prometheus/common v0.70's
	// zero-value TextParser panics rather than defaulting.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(rec.Body)
	Expect(err).NotTo(HaveOccurred())

	out := map[string]float64{}
	for name, family := range families {
		for _, m := range family.GetMetric() {
			key := name
			for _, l := range m.GetLabel() {
				key += "|" + l.GetName() + "=" + l.GetValue()
			}
			switch {
			case m.GetCounter() != nil:
				out[key] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				out[key] = m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				out[key] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return out
}

var _ = Describe("Synthetic metrics exporter", func() {
	// §6.4 step 2: the synthetic source must advance "enough to exercise
	// scrape+relabel+write for real". A counter that never moves makes every
	// rate() in the user's downstream rules zero, and the sandbox reports a
	// working pipeline as if it were producing nothing.
	It("advances its counter between scrapes", func() {
		exporter := NewSyntheticExporter()
		const key = "shepherd_sim_requests_total|method=GET|path=/api/health"

		before := scrapeSynthetic(exporter)
		Expect(before).To(HaveKey(key))

		exporter.Advance()
		after := scrapeSynthetic(exporter)

		Expect(after[key]).To(BeNumerically(">", before[key]),
			"the counter must move between scrapes or every downstream rate() is zero")
	})

	It("exposes every counter series from the first scrape, so no series looks like a reset mid-run", func() {
		exporter := NewSyntheticExporter()
		first := scrapeSynthetic(exporter)
		for _, s := range syntheticRequestSeries {
			Expect(first).To(HaveKey("shepherd_sim_requests_total|method=" + s.method + "|path=" + s.path))
		}
	})

	It("serves a gauge that goes down as well as up", func() {
		// A gauge that only rises is indistinguishable from a counter to any
		// downstream rule, so the sawtooth is the behaviour, not decoration.
		exporter := NewSyntheticExporter()
		var values []float64
		for range 10 {
			values = append(values, scrapeSynthetic(exporter)["shepherd_sim_queue_depth"])
			exporter.Advance()
		}
		var decreased bool
		for i := 1; i < len(values); i++ {
			if values[i] < values[i-1] {
				decreased = true
			}
		}
		Expect(decreased).To(BeTrue(), "queue depth never decreased across %v", values)
	})

	It("observes into its histogram on every advance", func() {
		exporter := NewSyntheticExporter()
		before := scrapeSynthetic(exporter)["shepherd_sim_request_duration_seconds"]
		exporter.Advance()
		Expect(scrapeSynthetic(exporter)["shepherd_sim_request_duration_seconds"]).To(BeNumerically(">", before))
	})

	It("exposes no Go runtime or process series, so a captured table shows only the user's pipeline", func() {
		metrics := scrapeSynthetic(NewSyntheticExporter())
		for key := range metrics {
			Expect(key).NotTo(HavePrefix("go_"))
			Expect(key).NotTo(HavePrefix("process_"))
		}
	})
})

var _ = Describe("Synthetic log emitter", func() {
	It("writes each fixture to the exact file the transform's loki.source.file stub tails", func() {
		dir := GinkgoT().TempDir()
		emitter, err := NewLogEmitter(dir, []string{"k8s-pod-logs"}, DefaultLogEmitInterval)
		Expect(err).NotTo(HaveOccurred())
		Expect(emitter.Prepare()).To(Succeed())
		emitter.emit()

		// The path is built by the same function the transform calls, so a
		// rename cannot leave the tailer pointed at a file nobody writes.
		path := filepath.Join(dir, simulate.StubLogFileName("k8s-pod-logs"))
		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())

		lines, ok := simulate.StubLogLines("k8s-pod-logs")
		Expect(ok).To(BeTrue())
		for _, line := range lines {
			Expect(string(content)).To(ContainSubstring(line))
		}
	})

	It("truncates the fixture file so a run never tails the previous run's lines", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, simulate.StubLogFileName("docker-logs"))
		Expect(os.WriteFile(path, []byte("stale line from an earlier run\n"), 0o600)).To(Succeed())

		emitter, err := NewLogEmitter(dir, []string{"docker-logs"}, DefaultLogEmitInterval)
		Expect(err).NotTo(HaveOccurred())
		Expect(emitter.Prepare()).To(Succeed())

		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(BeEmpty())
	})

	It("refuses an unknown fixture rather than silently writing nothing", func() {
		_, err := NewLogEmitter(GinkgoT().TempDir(), []string{"no-such-fixture"}, DefaultLogEmitInterval)
		Expect(err).To(MatchError(ContainSubstring("unknown log fixture")))
	})
})
