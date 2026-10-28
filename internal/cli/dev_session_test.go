package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
)

var _ = Describe("devSessionToken", func() {
	It("carries the persona label plus 24 random bytes", func() {
		tok := devSessionToken("viewer-platform")
		Expect(tok).To(HavePrefix("dev-viewer-platform-"))
		raw, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(tok, "dev-viewer-platform-"))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(HaveLen(24))
	})

	It("is unique over many calls", func() {
		seen := map[string]bool{}
		for range 500 {
			tok := devSessionToken("appadmin")
			Expect(seen).NotTo(HaveKey(tok))
			seen[tok] = true
		}
	})
})

var _ = Describe("dev create-session", Label("integration"), func() {
	var (
		ctx context.Context
		st  *store.Store
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbURL := devUsersPG.IsolatedDB(ctx, GinkgoTB())
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", dbURL)
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
	})

	runCreateSession := func() string {
		var out bytes.Buffer
		DeferCleanup(func() {
			rootCmd.SetOut(nil)
			rootCmd.SetErr(nil)
			rootCmd.SetArgs(nil)
			Expect(devCreateSessionCmd.Flags().Set("persona", "appadmin")).To(Succeed())
		})
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"dev", "create-session", "--persona", "viewer-platform"})
		Expect(rootCmd.Execute()).To(Succeed(), out.String())

		m := regexp.MustCompile(`(?m)^session_id=(\S+) persona=viewer-platform ttl=1h0m0s$`).FindStringSubmatch(out.String())
		Expect(m).To(HaveLen(2), "unexpected output: %s", out.String())
		Expect(out.String()).To(ContainSubstring("# Set cookie: shepherd_session=<session_id>"))
		return m[1]
	}

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

	authStatus := func(v string) int {
		h := auth.NewLocalAdmin(&config.Config{Auth: config.AuthConfig{InsecureCookies: true}}, st, slog.Default())
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(&http.Cookie{Name: "shepherd_session", Value: v})
		rr := httptest.NewRecorder()
		h.SessionMiddleware(auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))).ServeHTTP(rr, req)
		return rr.Code
	}

	It("stores only the digest of the printed token, which signs in while the stored id does not", func() {
		token := runCreateSession()
		Expect(token).To(HavePrefix("dev-viewer-platform-"))

		ids := storedIDs()
		Expect(ids).To(ConsistOf(auth.HashSessionToken(token)))
		Expect(ids).NotTo(ContainElement(token))

		Expect(authStatus(token)).To(Equal(http.StatusOK))
		Expect(authStatus(ids[0])).To(Equal(http.StatusUnauthorized))
	})
})
