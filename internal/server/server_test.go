package server_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"shepherd/internal/spa"
)

var _ = Describe("metrics listeners", func() {
	It("metrics handler is not registered on the main chi router", func() {
		r := chi.NewRouter()
		apiGuard := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("{\"error\":{\"code\":\"not_found\"}}")) //nolint:errcheck // error response write
		})
		r.Handle("/metrics", apiGuard)

		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
	})

	It("metrics mux serves prometheus on /metrics", func() {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.Handler())

		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		metricsMux.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
	})
})

var _ = Describe("API prefix guard", func() {
	var r *chi.Mux

	BeforeEach(func() {
		r = chi.NewRouter()
		apiGuard := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("{\"error\":{\"code\":\"not_found\"}}")) //nolint:errcheck
		})
		r.Handle("/api", apiGuard)
		r.Handle("/api/*", apiGuard)
		r.Handle("/auth/*", apiGuard)
		r.Handle("/collector.v1.*", apiGuard)
		r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	DescribeTable("unmatched reserved paths return 404 JSON",
		func(path string) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusNotFound), path)
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"), path)
			Expect(rec.Body.String()).To(ContainSubstring("not_found"), path)
			Expect(rec.Body.String()).NotTo(ContainSubstring("<html"), path)
		},
		Entry("GET /api/v2/nope", "/api/v2/nope"),
		Entry("GET /api/", "/api/"),
		Entry("GET /api (bare)", "/api"),
		Entry("GET /auth/nope", "/auth/nope"),
	)

	It("real routes still respond", func() {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
	})
})

var _ = Describe("SPA cache headers", func() {
	It("unknown /assets/* path returns 404 not index.html", func() {
		h := spa.Handler()
		req := httptest.NewRequest(http.MethodGet, "/assets/nope.js", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<html"))
	})

	It("unknown client-side route returns index.html with no-cache", func() {
		h := spa.Handler()
		req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Header().Get("Cache-Control")).To(Equal("no-cache"))
	})
})
