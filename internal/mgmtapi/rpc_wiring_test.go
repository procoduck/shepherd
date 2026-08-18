package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
)

// newRPCWiringRouter builds a router shaped like internal/server/server.go's
// production wiring: session + CSRF middleware, MountRPC'd Connect service
// handlers, the /shepherd.mgmt.v1.* 404 guard, and a stand-in "SPA" fallback
// mounted last — the exact ordering that lets us prove an unknown
// shepherd.mgmt.v1 path is guarded instead of falling through to the SPA.
func newRPCWiringRouter(st *store.Store, authHandler *auth.Handler, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	apiGuard := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`)) //nolint:errcheck // error response write
	})

	r.Group(func(r chi.Router) {
		r.Use(authHandler.SessionMiddleware)
		r.Use(auth.CSRFMiddleware)
		mgmtapi.MountRPC(r, st, cfg, nil, slog.Default())
	})

	r.Handle("/shepherd.mgmt.v1.*", apiGuard)
	r.Mount("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>spa fallback</html>")) //nolint:errcheck // stand-in SPA response
	}))

	return r
}

var _ = Describe("shepherd.mgmt.v1 RPC wiring", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())

		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// postConnect issues a Connect unary POST. X-Requested-With is always set
	// (even for the unauthenticated case) so the assertion exercises the
	// Connect authz path rather than being short-circuited by CSRFMiddleware,
	// which the design doc requires enforced ahead of every Connect call.
	postConnect := func(procedure string, cookie *http.Cookie) *http.Response {
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader("{}"))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	connectErrorCode := func(resp *http.Response) string {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload struct {
			Code string `json:"code"`
		}
		Expect(json.Unmarshal(body, &payload)).To(Succeed())
		return payload.Code
	}

	It("rejects an unauthenticated Connect call with the Connect unauthenticated code", func() {
		resp := postConnect("/shepherd.mgmt.v1.MeService/GetMe", nil)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(connectErrorCode(resp)).To(Equal("unauthenticated"))
	})

	// NOTE: every shepherd.mgmt.v1 service method is now implemented (the
	// AdminService/MeService migration — the last remaining stubs — landed
	// in the same change that removed this file's former "authenticated
	// call to a stubbed method returns Connect unimplemented" case). A
	// procedure path not present on a registered service's generated mux
	// resolves to a plain http.NotFound, not a Connect-formatted
	// unimplemented error (see mgmtv1connect's generated
	// New<X>ServiceHandler default case), so that guarantee can no longer
	// be exercised through production MountRPC wiring with a real
	// procedure name. The unauthenticated-rejection and unknown-path 404
	// guard cases below still cover the wiring this suite exists to prove.

	It("404s an unknown /shepherd.mgmt.v1.* path as JSON instead of falling through to the SPA handler", func() {
		resp, err := http.Get(server.URL + "/shepherd.mgmt.v1.NoSuchService/Foo")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup

		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/json"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"not_found"`))
		Expect(string(body)).NotTo(ContainSubstring("spa fallback"))
	})
})
