package simsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/prometheus/model/value"

	"shepherd/internal/simulate"
)

// capturingConfig is a config whose only destination is the harness, so it
// passes the endpoint allowlist.
func capturingConfig() string {
	return fmt.Sprintf(`
prometheus.remote_write "cap" {
  endpoint {
    url = %q
  }
}
`, "http://shepherd-simulator:9110"+simulate.CapturePathPrometheus)
}

func doJSON(handler http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	GinkgoHelper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

var _ = Describe("Control API", func() {
	var (
		cfg     Config
		queue   *Queue
		handler http.Handler
	)

	BeforeEach(func() {
		cfg = testConfig(GinkgoT().TempDir())
		queue = newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(queue.Close)
		handler = NewServer(cfg, queue, nil, nil).Handler()
	})

	It("accepts a run and answers 202 with a pollable id", func() {
		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig(), DurationSeconds: 15}, "")
		Expect(rec.Code).To(Equal(http.StatusAccepted))

		var run Run
		Expect(json.Unmarshal(rec.Body.Bytes(), &run)).To(Succeed())
		Expect(run.ID).NotTo(BeEmpty())
		Expect(run.State).To(Equal(StateQueued))
		Expect(run.DurationSeconds).To(Equal(15))

		Eventually(func() RunState {
			poll := doJSON(handler, http.MethodGet, "/v1/runs/"+run.ID, nil, "")
			Expect(poll.Code).To(Equal(http.StatusOK))
			var view runView
			Expect(json.Unmarshal(poll.Body.Bytes(), &view)).To(Succeed())
			return view.State
		}).Should(Equal(StateCompleted))
	})

	It("refuses a config naming an endpoint outside the harness", func() {
		leaky := `
prometheus.remote_write "leak" {
  endpoint {
    url = "https://metrics.example.com/api/v1/write"
  }
}
`
		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: leaky}, "")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		var apiErr APIError
		Expect(json.Unmarshal(rec.Body.Bytes(), &apiErr)).To(Succeed())
		Expect(apiErr.Code).To(Equal("endpoint_not_allowed"))
		Expect(queue.IDs()).To(BeEmpty(), "a refused config must not create a run")
	})

	It("refuses a config that Alloy could not parse", func() {
		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: `prometheus.remote_write "cap" {`}, "")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		var apiErr APIError
		Expect(json.Unmarshal(rec.Body.Bytes(), &apiErr)).To(Succeed())
		Expect(apiErr.Code).To(Equal("invalid_config"))
	})

	It("refuses an empty config", func() {
		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: "   "}, "")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("answers 413 for a config over the size cap", func() {
		cfg.MaxConfigBytes = 512
		handler = NewServer(cfg, queue, nil, nil).Handler()

		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: strings.Repeat("x", 4096)}, "")
		Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
	})

	It("answers 429 with Retry-After once the backlog is full", func() {
		cfg.QueueDepth = 0
		full := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome { return runOutcome{} })
		DeferCleanup(full.Close)
		handler = NewServer(cfg, full, nil, nil).Handler()

		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, "")
		Expect(rec.Code).To(Equal(http.StatusTooManyRequests))
		Expect(rec.Header().Get("Retry-After")).NotTo(BeEmpty())
	})

	It("answers 404 for an unknown run on both poll and cancel", func() {
		Expect(doJSON(handler, http.MethodGet, "/v1/runs/nope", nil, "").Code).To(Equal(http.StatusNotFound))
		Expect(doJSON(handler, http.MethodDelete, "/v1/runs/nope", nil, "").Code).To(Equal(http.StatusNotFound))
	})

	It("cancels a run and answers 204", func() {
		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, "")
		var run Run
		Expect(json.Unmarshal(rec.Body.Bytes(), &run)).To(Succeed())

		Expect(doJSON(handler, http.MethodDelete, "/v1/runs/"+run.ID, nil, "").Code).To(Equal(http.StatusNoContent))

		poll := doJSON(handler, http.MethodGet, "/v1/runs/"+run.ID, nil, "")
		var view runView
		Expect(json.Unmarshal(poll.Body.Bytes(), &view)).To(Succeed())
		Expect(view.State).To(Equal(StateCanceled))
	})

	It("reports a countdown while the run is live and zero once it is terminal", func() {
		// The countdown is what §6.4 step 4's "running 30s countdown" state
		// renders; the client cannot compute it without the server's clock.
		release := make(chan struct{})
		blocking := newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			<-release
			return runOutcome{}
		})
		DeferCleanup(blocking.Close)
		handler = NewServer(cfg, blocking, nil, nil).Handler()

		rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, "")
		var run Run
		Expect(json.Unmarshal(rec.Body.Bytes(), &run)).To(Succeed())

		poll := doJSON(handler, http.MethodGet, "/v1/runs/"+run.ID, nil, "")
		var live runView
		Expect(json.Unmarshal(poll.Body.Bytes(), &live)).To(Succeed())
		Expect(live.RemainingSeconds).To(BeNumerically(">", 0))

		close(release)

		Eventually(func() RunState {
			p := doJSON(handler, http.MethodGet, "/v1/runs/"+run.ID, nil, "")
			var v runView
			Expect(json.Unmarshal(p.Body.Bytes(), &v)).To(Succeed())
			return v.State
		}).Should(Equal(StateCompleted))

		final := doJSON(handler, http.MethodGet, "/v1/runs/"+run.ID, nil, "")
		var done runView
		Expect(json.Unmarshal(final.Body.Bytes(), &done)).To(Succeed())
		Expect(done.RemainingSeconds).To(Equal(0))
	})

	It("answers a poll whose results carry NaN, ±Inf and stale-marker samples with 200 and a decodable body", func() {
		// Real Alloy self-metrics carry NaN (an empty summary's quantiles):
		// a prometheus.exporter.self pipeline used to fail every run with
		// "json: unsupported value: NaN" and a truncated 200 body.
		var nanQueue *Queue
		nanQueue = newTestQueue(cfg, func(context.Context, runnerOptions) runOutcome {
			s := nanQueue.harness.activeSink()
			s.addSample(map[string]string{"__name__": "q", "quantile": "0.5"}, math.NaN(), 1000)
			s.addSample(map[string]string{"__name__": "q", "quantile": "0.5"}, math.Inf(1), 2000)
			s.addSample(map[string]string{"__name__": "neg"}, math.Inf(-1), 1000)
			s.addSample(map[string]string{"__name__": "stale"}, math.Float64frombits(value.StaleNaN), 1000)
			s.addSample(map[string]string{"__name__": "ok"}, 2.5, 1000)
			return runOutcome{}
		})
		DeferCleanup(nanQueue.Close)
		handler = NewServer(cfg, nanQueue, nil, nil).Handler()
		srv := httptest.NewServer(handler)
		DeferCleanup(srv.Close)
		client := simulate.NewClient(srv.URL, "", nil)

		run, err := client.Start(context.Background(), simulate.ClientStartRequest{Config: capturingConfig()})
		Expect(err).NotTo(HaveOccurred())

		var final simulate.ClientRun
		Eventually(func() string {
			var getErr error
			final, getErr = client.Get(context.Background(), run.ID)
			Expect(getErr).NotTo(HaveOccurred(), "the poll must decode, not fail with a truncated body")
			return final.State
		}).Should(Equal(string(StateCompleted)))

		Expect(final.Results).NotTo(BeNil())
		byName := map[string]simulate.ClientSeries{}
		for _, s := range final.Results.Series {
			byName[s.Name] = s
		}
		Expect(byName).To(HaveLen(4))
		Expect(math.IsNaN(float64(byName["q"].FirstValue))).To(BeTrue())
		Expect(value.IsStaleNaN(float64(byName["q"].FirstValue))).To(BeFalse())
		Expect(math.IsInf(float64(byName["q"].LastValue), 1)).To(BeTrue())
		Expect(math.IsInf(float64(byName["neg"].LastValue), -1)).To(BeTrue())
		Expect(value.IsStaleNaN(float64(byName["stale"].LastValue))).To(BeTrue())
		Expect(float64(byName["ok"].LastValue)).To(Equal(2.5))
	})

	It("reports a response it cannot encode as a 500 with a JSON error body, never a truncated 200", func() {
		srv := NewServer(cfg, queue, nil, nil)
		rec := httptest.NewRecorder()
		srv.writeJSON(rec, http.StatusOK, map[string]any{"unencodable": make(chan int)})

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
		var apiErr APIError
		Expect(json.Unmarshal(rec.Body.Bytes(), &apiErr)).To(Succeed(), "body: %q", rec.Body.String())
		Expect(apiErr.Code).To(Equal("encode_failed"))
		Expect(apiErr.Message).NotTo(BeEmpty())

		// And the client surfaces it as a typed API error naming the failure,
		// not "unexpected end of JSON input".
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			srv.writeJSON(w, http.StatusOK, map[string]any{"unencodable": make(chan int)})
		}))
		DeferCleanup(ts.Close)
		_, err := simulate.NewClient(ts.URL, "", nil).Get(context.Background(), "any")
		var clientErr *simulate.ClientAPIError
		Expect(errors.As(err, &clientErr)).To(BeTrue(), "got %v", err)
		Expect(clientErr.HTTPStatus).To(Equal(http.StatusInternalServerError))
		Expect(clientErr.Code).To(Equal("encode_failed"))
	})

	Context("with a bearer token configured", func() {
		BeforeEach(func() {
			cfg.Token = "sim-token-for-tests"
			handler = NewServer(cfg, queue, nil, nil).Handler()
		})

		It("rejects an unauthenticated run submission", func() {
			rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, "")
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(queue.IDs()).To(BeEmpty())
		})

		It("rejects a wrong token", func() {
			rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, "wrong")
			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		})

		It("accepts the configured token", func() {
			rec := doJSON(handler, http.MethodPost, "/v1/runs", StartRequest{Config: capturingConfig()}, cfg.Token)
			Expect(rec.Code).To(Equal(http.StatusAccepted))
		})

		It("lets the real simulate.Client authenticate against this handler with the configured token", func() {
			// The handler-side specs above drive raw HTTP requests
			// (doJSON); this drives the actual client the worker uses
			// (internal/simulate.Client), against a real listening server,
			// proving the two sides' token wiring actually interoperate.
			srv := httptest.NewServer(handler)
			DeferCleanup(srv.Close)
			client := simulate.NewClient(srv.URL, cfg.Token, nil)

			run, err := client.Start(context.Background(), simulate.ClientStartRequest{
				Config: capturingConfig(), DurationSeconds: 15,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).NotTo(BeEmpty())
		})

		It("reports a rejected bearer token as a typed *ClientAPIError with HTTP 401, not a transport error", func() {
			// One-line-revert red proof (control already holds today):
			// commenting out the ConstantTimeCompare check at server.go:75
			// (leaving the handler accepting any token) turns this spec's
			// `errors.As` into false — no error at all comes back — and it
			// was run that way to confirm, then restored.
			srv := httptest.NewServer(handler)
			DeferCleanup(srv.Close)
			client := simulate.NewClient(srv.URL, "wrong-token", nil)

			_, err := client.Start(context.Background(), simulate.ClientStartRequest{Config: capturingConfig()})
			Expect(err).To(HaveOccurred())

			var apiErr *simulate.ClientAPIError
			Expect(errors.As(err, &apiErr)).To(BeTrue(),
				"a rejected token must decode as a well-formed ClientAPIError, not fall through to ErrSimulatorUnreachable's generic 'non-2xx' message")
			Expect(apiErr.HTTPStatus).To(Equal(http.StatusUnauthorized))
			Expect(apiErr.Code).To(Equal("unauthorized"))
		})

		It("leaves the probes unauthenticated so a kubelet can reach them", func() {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
		})
	})

	It("reports not-ready when the readiness probe fails", func() {
		// A simulator that reports ready without an executable Alloy answers
		// every run with a failure the user cannot act on.
		handler = NewServer(cfg, queue, func() error { return fmt.Errorf("alloy binary missing") }, nil).Handler()

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(rec.Body.String()).To(ContainSubstring("alloy binary missing"))
	})

	It("readiness fails against the default config, whose Alloy binary is absent in a unit test", func() {
		Expect(readiness(cfg)).To(HaveOccurred())
	})
})

var _ = Describe("Simulator configuration", func() {
	It("defaults every SIM_* key when the environment is empty", func() {
		cfg, err := LoadConfig(func(string) string { return "" })
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Listen).To(Equal(DefaultListen))
		Expect(cfg.CaptureListen).To(Equal(DefaultCaptureListen))
		Expect(cfg.SyntheticListen).To(Equal(DefaultSyntheticListen))
		Expect(cfg.DefaultDuration).To(Equal(30 * time.Second))
		Expect(cfg.MaxDuration).To(Equal(120 * time.Second))
		Expect(cfg.RunTTL).To(Equal(5 * time.Minute))
		Expect(cfg.ResultTTL).To(Equal(time.Hour))
		Expect(cfg.SATokenPath).To(Equal(DefaultSATokenPath))
		// The default allowlist must cover the compose service name, or every
		// run the transform produces is refused by the guard.
		Expect(cfg.AllowedHosts).To(ContainElement("shepherd-simulator"))
	})

	It("rejects a maximum duration below the default rather than clamping to a contradiction", func() {
		_, err := LoadConfig(func(key string) string {
			switch key {
			case "SIM_DEFAULT_DURATION":
				return "60s"
			case "SIM_MAX_DURATION":
				return "30s"
			}
			return ""
		})
		Expect(err).To(MatchError(ContainSubstring("SIM_MAX_DURATION")))
	})

	It("rejects an unparseable duration rather than silently defaulting", func() {
		_, err := LoadConfig(func(key string) string {
			if key == "SIM_MAX_DURATION" {
				return "two minutes"
			}
			return ""
		})
		Expect(err).To(MatchError(ContainSubstring("SIM_MAX_DURATION")))
	})
})
