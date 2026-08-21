package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/server"
	"shepherd/internal/spa"
	"shepherd/internal/store"
	"shepherd/internal/validate"
)

// productionRouter builds the real route tree via server.newRouter with the
// non-routing collaborators zeroed out: route registration never touches the
// database, so a zero store is enough to prove mounting and guard ordering.
func productionRouter() http.Handler {
	cfg := &config.Config{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return server.NewRouter(cfg, &store.Store{}, nil, nil, validate.New(&cfg.Validate), spa.BuildInfo{}, logger)
}

var _ = Describe("metrics listeners", func() {
	It("metrics handler is not registered on the main router", func() {
		r := productionRouter()

		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(rec.Body.String()).To(ContainSubstring("not_found"))
	})

	It("metrics mux serves prometheus on /metrics", func() {
		req := httptest.NewRequest("GET", "/metrics", nil)
		rec := httptest.NewRecorder()
		server.NewMetricsMux().ServeHTTP(rec, req)

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
	})
})

var _ = Describe("API prefix guard", func() {
	var r http.Handler

	BeforeEach(func() {
		r = productionRouter()
	})

	DescribeTable("unmatched reserved paths return 404 JSON, never the SPA",
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
		Entry("GET /collector.v1.Nope/Nope", "/collector.v1.Nope/Nope"),
		Entry("GET /shepherd.mgmt.v1.Nope/Nope", "/shepherd.mgmt.v1.Nope/Nope"),
	)

	It("real routes still respond", func() {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It("the SPA fallback still serves client-side routes after the guards", func() {
		req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Cache-Control")).To(Equal("no-cache"))
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
