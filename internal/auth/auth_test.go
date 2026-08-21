package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/oauth2"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

var sharedPG *testutil.SharedPostgres

func TestAuth(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("Session logout", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		h      *auth.Handler
		cfg    *config.Config
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		cfg = &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h = auth.NewLocalAdmin(cfg, st, slog.Default())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	It("logout deletes the session row", func() {
		sessionID := "logout-session"
		expiresAt := time.Now().Add(time.Hour)
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "user-oid", Email: "user@example.com", DisplayName: "User",
			GroupIds: json.RawMessage(`[]`), ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		req := httptest.NewRequest(http.MethodGet, "/logout", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: sessionID})
		rr := httptest.NewRecorder()
		h.LogoutHandler(rr, req)

		_, err = st.Queries.GetSessionByID(ctx, sessionID)
		Expect(err).To(HaveOccurred())
		cookies := rr.Result().Cookies()
		Expect(cookies).To(HaveLen(1))
		Expect(cookies[0].MaxAge).To(Equal(-1))
		Expect(cookies[0].Path).To(Equal("/"))
	})

	It("a replayed session cookie after logout is rejected", func() {
		sessionID := "replayed-session"
		expiresAt := time.Now().Add(time.Hour)
		_, err := st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "user-oid", Email: "user@example.com", DisplayName: "User",
			GroupIds: json.RawMessage(`[]`), ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		logoutReq := httptest.NewRequest(http.MethodGet, "/logout", nil)
		logoutReq.AddCookie(&http.Cookie{Name: "shepherd_session", Value: sessionID})
		h.LogoutHandler(httptest.NewRecorder(), logoutReq)

		replayReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		replayReq.AddCookie(&http.Cookie{Name: "shepherd_session", Value: sessionID})
		rr := httptest.NewRecorder()
		h.SessionMiddleware(auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(rr, replayReq)

		Expect(rr.Code).To(Equal(http.StatusUnauthorized))
		Expect(rr.Body.String()).To(MatchJSON(`{"error":{"code":"unauthenticated","message":"not authenticated"}}`))
	})
})

var _ = Describe("CSRFMiddleware", func() {
	var handler http.Handler

	BeforeEach(func() {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		handler = auth.CSRFMiddleware(inner)
	})

	It("allows GET requests without X-Requested-With", func() {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		Expect(rr.Code).To(Equal(http.StatusOK))
	})

	It("mutating request without X-Requested-With is rejected 403", func() {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			req := httptest.NewRequest(method, "/api/test", nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			Expect(rr.Code).To(Equal(http.StatusForbidden), "expected 403 for %s without CSRF header", method)
		}
	})

	It("mutating request WITH X-Requested-With is allowed", func() {
		req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		Expect(rr.Code).To(Equal(http.StatusOK))
	})
})

var _ = Describe("PKCE in LoginHandler", func() {
	// Full PKCE round-trip (challenge → callback → token exchange) is e2e
	// §18.4 territory; this spec drives the real LoginHandler against a
	// static oauth2 config, which needs no live provider.
	It("authorize URL contains code_challenge + method S256 and the cookie carries state|verifier", func() {
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h := auth.NewOIDCTestHandler(cfg, &oauth2.Config{
			ClientID:    "client-id",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://issuer.example/authorize"},
			RedirectURL: "https://shepherd.example/auth/callback",
			Scopes:      []string{"openid"},
		})

		req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
		rr := httptest.NewRecorder()
		h.LoginHandler(rr, req)

		Expect(rr.Code).To(Equal(http.StatusFound))
		loc, err := url.Parse(rr.Header().Get("Location"))
		Expect(err).NotTo(HaveOccurred())
		q := loc.Query()
		Expect(q.Get("code_challenge_method")).To(Equal("S256"))
		Expect(q.Get("code_challenge")).NotTo(BeEmpty())
		Expect(q.Get("state")).NotTo(BeEmpty())

		var stateCookie *http.Cookie
		for _, c := range rr.Result().Cookies() {
			if c.Name == "oidc_state" {
				stateCookie = c
			}
		}
		Expect(stateCookie).NotTo(BeNil(), "LoginHandler must set the oidc_state cookie")
		parts := strings.SplitN(stateCookie.Value, "|", 2)
		Expect(parts).To(HaveLen(2), "cookie must carry state|verifier")
		Expect(parts[0]).To(Equal(q.Get("state")), "cookie state must match the authorize URL state")

		// The challenge must actually derive from the cookie's verifier:
		// S256(verifier) per RFC 7636 §4.2 — otherwise the callback's
		// token exchange could never succeed.
		sum := sha256.Sum256([]byte(parts[1]))
		Expect(base64.RawURLEncoding.EncodeToString(sum[:])).To(Equal(q.Get("code_challenge")))
	})
})
