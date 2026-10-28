package auth_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// D7: an OIDC session must not outlive the ID token that backed its
// creation, even when the session row's own TTL (expires_at, the sliding
// shepherd_session cookie lifetime) has not yet run out. Before this spec,
// SessionMiddleware trusted GetSessionByID's expires_at filter alone;
// sessions.id_token_expires — written at login (auth.go
// createSessionAndSetCookie) from the ID token's own exp claim — was never
// read back.
var _ = Describe("Session expiry", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		h      *auth.Handler
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		h = auth.NewLocalAdmin(cfg, st, slog.Default())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	authedRequest := func(sessionID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: sessionID})
		rr := httptest.NewRecorder()
		h.SessionMiddleware(auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(rr, req)
		return rr
	}

	It("rejects a session whose ID token has expired even though expires_at (the row TTL) has not", func() {
		sessionID := "session-expiry-id-token-expired"
		groupsJSON, err := json.Marshal([]string{})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: auth.HashSessionToken(sessionID), UserOid: "user-oid", Email: "user@example.com", DisplayName: "User",
			GroupIds:       groupsJSON,
			IDTokenExpires: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
			ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:         "oidc",
		})
		Expect(err).NotTo(HaveOccurred())

		rr := authedRequest(sessionID)
		Expect(rr.Code).To(Equal(http.StatusUnauthorized))

		_, err = st.Queries.GetSessionByID(ctx, auth.HashSessionToken(sessionID))
		Expect(err).To(HaveOccurred(), "a session past its ID token's expiry should be deleted, not merely ignored")
	})

	It("still accepts a session whose ID token has not expired", func() {
		sessionID := "session-expiry-id-token-live"
		groupsJSON, err := json.Marshal([]string{})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: auth.HashSessionToken(sessionID), UserOid: "user-oid", Email: "user@example.com", DisplayName: "User",
			GroupIds:       groupsJSON,
			IDTokenExpires: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:         "oidc",
		})
		Expect(err).NotTo(HaveOccurred())

		rr := authedRequest(sessionID)
		Expect(rr.Code).To(Equal(http.StatusOK))
	})

	It("still accepts a local session, which has no ID token and so no id_token_expires", func() {
		sessionID := "session-expiry-local-no-id-token"
		groupsJSON, err := json.Marshal([]string{})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: auth.HashSessionToken(sessionID), UserOid: "local:admin", Email: "admin@example.com", DisplayName: "Admin",
			GroupIds:  groupsJSON,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:    "local",
		})
		Expect(err).NotTo(HaveOccurred())

		rr := authedRequest(sessionID)
		Expect(rr.Code).To(Equal(http.StatusOK))
	})
})
