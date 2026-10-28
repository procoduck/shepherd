package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

var _ = Describe("Session token digests", func() {
	It("hashes a token to the lowercase hex SHA-256 of its bytes", func() {
		sum := sha256.Sum256([]byte("test-session-a"))
		Expect(auth.HashSessionToken("test-session-a")).To(Equal(hex.EncodeToString(sum[:])))
		Expect(auth.HashSessionToken("test-session-a")).To(MatchRegexp(`^[0-9a-f]{64}$`))
		Expect(auth.HashSessionToken("test-session-a")).NotTo(Equal(auth.HashSessionToken("test-session-b")))
	})

	It("mints 24 random bytes, base64url-encoded, unique per call", func() {
		seen := map[string]bool{}
		for range 200 {
			tok := auth.NewSessionToken()
			raw, err := base64.URLEncoding.DecodeString(tok)
			Expect(err).NotTo(HaveOccurred())
			Expect(raw).To(HaveLen(24))
			Expect(seen).NotTo(HaveKey(tok))
			seen[tok] = true
		}
	})
})

var _ = Describe("Session storage", Label("integration"), func() {
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
		h = auth.NewLocalAdmin(&config.Config{Auth: config.AuthConfig{InsecureCookies: true}}, st, slog.Default())

		hash, err := auth.HashPassword("storage-test-password")
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: "storage", Email: "storage@example.com", DisplayName: "Storage",
			PasswordHash: hash, IsAppAdmin: false, MustChangePassword: false,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	storedIDs := func() []string {
		// RAW-SQL-OK: the spec reads the table directly to see what is at rest.
		rows, err := st.Pool().Query(ctx, `SELECT id FROM sessions`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			Expect(rows.Scan(&id)).To(Succeed())
			ids = append(ids, id)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return ids
	}

	// authStatus sends cookie value v through SessionMiddleware + RequireAuth.
	authStatus := func(v string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: v})
		rr := httptest.NewRecorder()
		h.SessionMiddleware(auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(rr, req)
		return rr.Code
	}

	signIn := func() string {
		resp := postLocalLogin(GinkgoTB(), h, "storage", "storage-test-password")
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		for _, c := range resp.Cookies() {
			if c.Name == "shepherd_session" && c.Value != "" {
				return c.Value
			}
		}
		Fail("no shepherd_session cookie on the sign-in response")
		return ""
	}

	It("stores only the digest of the cookie a local sign-in sets", func() {
		cookie := signIn()

		ids := storedIDs()
		Expect(ids).To(ConsistOf(auth.HashSessionToken(cookie)))
		Expect(ids).NotTo(ContainElement(cookie), "the raw cookie value must not be at rest")
	})

	It("authenticates the cookie and refuses the stored id replayed as a cookie", func() {
		cookie := signIn()
		ids := storedIDs()
		Expect(ids).To(HaveLen(1))

		Expect(authStatus(cookie)).To(Equal(http.StatusOK))
		Expect(authStatus(ids[0])).To(Equal(http.StatusUnauthorized),
			"a value read from the sessions table must not work as a cookie")
	})

	It("logout removes the row the cookie maps to", func() {
		cookie := signIn()
		Expect(storedIDs()).To(HaveLen(1))

		req := httptest.NewRequest(http.MethodGet, "/logout", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: cookie})
		h.LogoutHandler(httptest.NewRecorder(), req)

		Expect(storedIDs()).To(BeEmpty())
		Expect(authStatus(cookie)).To(Equal(http.StatusUnauthorized))
	})
})
