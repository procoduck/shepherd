package simsvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/golang/snappy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/prometheus/prompb"

	"shepherd/internal/simulate"
)

// The golden files are the bytes real grafana/alloy:v1.18.1 put on the wire —
// see testdata/README.md. Asserting decoded CONTENT (a series name, a
// relabel-produced label value, the exact log lines) is what makes these specs
// able to fail: a decoder that silently produced empty labels, or a receiver
// that stopped gunzipping, would pass a "no error" assertion.
func goldenBytes(name string) []byte {
	GinkgoHelper()
	raw, err := os.ReadFile("testdata/" + name)
	Expect(err).NotTo(HaveOccurred())
	return raw
}

// postTo drives one capture request through the real capture mux, which is the
// only way to prove the route the transform writes is the route the harness
// serves.
func postTo(h *Harness, path string, header http.Header, body []byte) *httptest.ResponseRecorder {
	GinkgoHelper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.CaptureMux().ServeHTTP(rec, req)
	return rec
}

var _ = Describe("Prometheus remote_write capture", func() {
	var harness *Harness
	var sink *sink

	BeforeEach(func() {
		harness = NewHarness(nil)
		sink = harness.begin()
	})

	It("decodes a real Alloy remote_write body into the series it carries, including the relabel-produced label", func() {
		header := http.Header{}
		header.Set("Content-Encoding", "snappy")
		header.Set("Content-Type", "application/x-protobuf")
		header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

		rec := postTo(harness, simulate.CapturePathPrometheus, header, goldenBytes("remote_write_alloy_v1.18.1.snappy"))
		Expect(rec.Code).To(Equal(http.StatusNoContent))

		results := sink.snapshot()
		Expect(results.SeriesTruncated).To(BeFalse())
		Expect(results.Series).To(HaveLen(576))

		var buildInfo *Series
		for i := range results.Series {
			if results.Series[i].Name == "alloy_build_info" {
				buildInfo = &results.Series[i]
			}
		}
		Expect(buildInfo).NotTo(BeNil(), "alloy_build_info must survive the decode")
		// sim_marker is produced by a discovery.relabel rule, not by the
		// exporter: its presence is the proof that scrape + relabel + write
		// ran end to end, which is the whole point of the sandbox.
		Expect(buildInfo.Labels).To(HaveKeyWithValue("sim_marker", "shepherd"))
		Expect(buildInfo.Labels).To(HaveKeyWithValue("job", "integrations/self"))
		Expect(buildInfo.Labels).To(HaveKeyWithValue("version", "v1.18.1"))
		Expect(buildInfo.SampleCount).To(Equal(1))
		Expect(buildInfo.LastValue).To(Equal(1.0))
		Expect(buildInfo.LastTimestampMS).To(BeNumerically(">", 0))
	})

	It("aggregates repeated samples of one series rather than duplicating it", func() {
		header := http.Header{}
		header.Set("Content-Type", "application/x-protobuf")
		body := snappyWriteRequest(
			prompb.TimeSeries{
				Labels:  []prompb.Label{{Name: "__name__", Value: "shepherd_sim_requests_total"}, {Name: "path", Value: "/api/health"}},
				Samples: []prompb.Sample{{Value: 3, Timestamp: 1000}, {Value: 7, Timestamp: 2000}},
			},
		)
		Expect(postTo(harness, simulate.CapturePathPrometheus, header, body).Code).To(Equal(http.StatusNoContent))

		results := sink.snapshot()
		Expect(results.Series).To(HaveLen(1))
		Expect(results.Series[0].SampleCount).To(Equal(2))
		Expect(results.Series[0].FirstValue).To(Equal(3.0))
		Expect(results.Series[0].LastValue).To(Equal(7.0))
		Expect(results.Series[0].FirstTimestampMS).To(Equal(int64(1000)))
		Expect(results.Series[0].LastTimestampMS).To(Equal(int64(2000)))
	})

	It("refuses a Remote-Write 2.0 body instead of decoding it as 1.0", func() {
		// 2.0 interns label strings in a symbol table, so a 1.0 decode
		// succeeds and yields EMPTY labels — a wrong answer rather than an
		// error, which is why the version header is checked.
		header := http.Header{}
		header.Set("X-Prometheus-Remote-Write-Version", "2.0.0")
		rec := postTo(harness, simulate.CapturePathPrometheus, header, goldenBytes("remote_write_alloy_v1.18.1.snappy"))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring("remote write 2.0 not supported"))
		Expect(sink.snapshot().Series).To(BeEmpty())
	})

	It("rejects a body that is not snappy-framed", func() {
		rec := postTo(harness, simulate.CapturePathPrometheus, http.Header{}, []byte("not snappy at all"))
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})
})

var _ = Describe("Loki push capture", func() {
	var harness *Harness
	var sink *sink

	BeforeEach(func() {
		harness = NewHarness(nil)
		sink = harness.begin()
	})

	It("captures the labels and lines of a real Alloy loki.write body", func() {
		header := http.Header{}
		header.Set("Content-Encoding", "snappy")
		header.Set("Content-Type", "application/x-protobuf")

		rec := postTo(harness, simulate.CapturePathLoki, header, goldenBytes("loki_push_alloy_v1.18.1.snappy"))
		Expect(rec.Code).To(Equal(http.StatusNoContent))

		results := sink.snapshot()
		Expect(results.LogLinesTruncated).To(BeFalse())
		Expect(results.LogLines).To(HaveLen(2))
		// Loki ships the label set as one pre-rendered string; these
		// assertions are what prove parseStreamLabels turned it back into a
		// map instead of handing the UI a single opaque label.
		Expect(results.LogLines[0].Labels).To(HaveKeyWithValue("job", "simlogs"))
		Expect(results.LogLines[0].Labels).To(HaveKeyWithValue("filename", "/sim/logs/app.log"))
		Expect(results.LogLines[0].Line).To(Equal(
			`{"level":"info","ts":"2026-08-18T10:00:00Z","msg":"request completed","method":"GET","path":"/api/health","status":200}`))
		Expect(results.LogLines[1].Line).To(Equal(
			`level=info ts=2026-08-18T10:01:00Z msg="query executed" table=pipelines rows=15`))
		Expect(results.LogLines[0].TimestampMS).To(BeNumerically(">", 0))
	})

	It("accepts the JSON push shape for hand-testing", func() {
		header := http.Header{}
		header.Set("Content-Type", "application/json")
		body, err := json.Marshal(map[string]any{
			"streams": []map[string]any{{
				"stream": map[string]string{"job": "manual"},
				"values": [][]string{{"1700000000000000000", "hand-written line"}},
			}},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(postTo(harness, simulate.CapturePathLoki, header, body).Code).To(Equal(http.StatusNoContent))
		results := sink.snapshot()
		Expect(results.LogLines).To(HaveLen(1))
		Expect(results.LogLines[0].Line).To(Equal("hand-written line"))
		Expect(results.LogLines[0].Labels).To(HaveKeyWithValue("job", "manual"))
	})

	It("keeps a comma inside a quoted label value intact", func() {
		labels := parseStreamLabels(`{filename="/var/log/a,b.log", job="simlogs"}`)
		Expect(labels).To(HaveKeyWithValue("filename", "/var/log/a,b.log"))
		Expect(labels).To(HaveKeyWithValue("job", "simlogs"))
	})
})

var _ = Describe("OTLP/HTTP capture", func() {
	var harness *Harness
	var sink *sink

	BeforeEach(func() {
		harness = NewHarness(nil)
		sink = harness.begin()
	})

	It("gunzips and decodes a real Alloy otlphttp metrics export", func() {
		header := http.Header{}
		header.Set("Content-Encoding", "gzip")
		header.Set("Content-Type", "application/x-protobuf")

		rec := postTo(harness, simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixMetrics, header, goldenBytes("otlp_metrics_alloy_v1.18.1.pb.gz"))
		Expect(rec.Code).To(Equal(http.StatusOK))
		// The exporter treats any other content type as a failed export and
		// drops the batch, which surfaces as an empty capture.
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/x-protobuf"))

		results := sink.snapshot()
		Expect(results.OTLP.MetricPoints).To(Equal(576))
		Expect(results.OTLP.ResourceAttributes).To(ContainElement("service.name"))
	})

	It("fails to decode the same body when the gzip layer is not removed", func() {
		// The measured failure this receiver exists to avoid: without the
		// gunzip branch the handler answers 400 and Alloy logs
		// "Exporting failed. Dropping data".
		_, err := decodeOTLPMetrics(goldenBytes("otlp_metrics_alloy_v1.18.1.pb.gz"))
		Expect(err).To(HaveOccurred())
	})

	It("accepts an uncompressed body too", func() {
		raw, err := readOTLPBody(gzipHeader(), bytes.NewReader(goldenBytes("otlp_metrics_alloy_v1.18.1.pb.gz")))
		Expect(err).NotTo(HaveOccurred())

		rec := postTo(harness, simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixMetrics, http.Header{}, raw)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(sink.snapshot().OTLP.MetricPoints).To(Equal(576))
	})
})

var _ = Describe("Capture routing", func() {
	// The transform writes these URLs into a user's config and the harness
	// routes them; the two halves meet nowhere else. A path typo produces a
	// run that is green everywhere and captures nothing, so the contract is
	// asserted in both directions.
	It("serves exactly the paths internal/simulate builds from a capture base URL", func() {
		const base = "http://shepherd-simulator:9110"
		harness := NewHarness(nil)
		harness.begin()

		for _, route := range CaptureRoutes() {
			req := httptest.NewRequest(http.MethodPost, base+route, bytes.NewReader(nil))
			rec := httptest.NewRecorder()
			harness.CaptureMux().ServeHTTP(rec, req)
			Expect(rec.Code).NotTo(Equal(http.StatusNotFound), "no handler registered for %s", route)
		}
	})

	It("routes every receiver kind the overlay can name", func() {
		routes := CaptureRoutes()
		Expect(routes).To(ContainElements(
			simulate.CapturePathPrometheus,
			simulate.CapturePathLoki,
			simulate.CapturePathPyroscope,
			simulate.CapturePathFaro,
			simulate.CapturePathSplunkHEC,
			simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixMetrics,
			simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixLogs,
			simulate.CapturePrefixOTLPHTTP+simulate.OTLPSuffixTraces,
		))
	})

	It("counts pyroscope, faro and splunk hec deliveries", func() {
		harness := NewHarness(nil)
		sink := harness.begin()
		for _, path := range []string{simulate.CapturePathPyroscope, simulate.CapturePathFaro, simulate.CapturePathSplunkHEC} {
			Expect(postTo(harness, path, http.Header{}, []byte("payload")).Code).To(Equal(http.StatusOK))
		}
		other := sink.snapshot().Other
		Expect(other.PyroscopeRequests).To(Equal(1))
		Expect(other.FaroRequests).To(Equal(1))
		Expect(other.SplunkHECRequests).To(Equal(1))
	})

	It("discards captures that arrive with no run active, so one run cannot inherit another's stragglers", func() {
		harness := NewHarness(nil)
		first := harness.begin()
		harness.end(first)

		header := http.Header{}
		header.Set("Content-Type", "application/x-protobuf")
		Expect(postTo(harness, simulate.CapturePathPrometheus, header, goldenBytes("remote_write_alloy_v1.18.1.snappy")).Code).
			To(Equal(http.StatusNoContent))

		second := harness.begin()
		Expect(second.snapshot().Series).To(BeEmpty())
	})
})

func gzipHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Encoding", "gzip")
	return h
}

func snappyWriteRequest(series ...prompb.TimeSeries) []byte {
	GinkgoHelper()
	req := prompb.WriteRequest{Timeseries: series}
	raw, err := req.Marshal()
	Expect(err).NotTo(HaveOccurred())
	return snappy.Encode(nil, raw)
}
