package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/server"
	"shepherd/internal/spa"
	"shepherd/internal/store"
	"shepherd/internal/testutil"
	"shepherd/internal/validate"
)

// The per-IP local sign-in throttle (internal/auth, loginPerIPBurst = 60) is
// keyed on r.RemoteAddr. That key is only worth anything if nothing in front of
// the handler lets the client choose it. chi's middleware.RealIP did exactly
// that — it rewrote r.RemoteAddr from X-Real-IP / X-Forwarded-For /
// True-Client-IP before any handler ran — so a client rotating the header got a
// fresh per-IP bucket on every request and could spray passwords across
// usernames without ever tripping the per-IP limit.
//
// This drives the PRODUCTION router (server.newRouter, the same middleware
// chain New mounts), not the handler, because the bypass lived in the chain:
// a handler-level test cannot see it.
var _ = Describe("local sign-in per-IP throttle through the production router", Ordered, Label("integration"), func() {
	var (
		pg     *testutil.SharedPostgres
		st     *store.Store
		router http.Handler
	)

	BeforeAll(func() {
		ctx := context.Background()
		var err error
		pg, err = testutil.StartSharedPostgres(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pg.Terminate(context.Background())).To(Succeed()) })
		Expect(store.MigrateUp(ctx, pg.RootURL)).To(Succeed())

		st, err = store.New(ctx, &config.DatabaseConfig{URL: pg.RootURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		authHandler := auth.NewLocalAdmin(cfg, st, logger)
		router = server.NewRouter(cfg, st, nil, authHandler, validate.New(&cfg.Validate), spa.BuildInfo{}, logger, func() bool { return false })
	})

	It("is not reset by a client-chosen X-Real-IP / X-Forwarded-For / True-Client-IP", func() {
		// Every attempt uses a different username, so the per-login bucket
		// (burst 10) never trips: only the per-IP bucket can produce a 429.
		// Every attempt also claims a different client address in all three
		// headers RealIP honoured. The TCP peer (httptest's fixed RemoteAddr,
		// i.e. the ingress in a real deployment) is the same throughout.
		//
		// The per-IP bucket holds 60 and refills at 1/s; each rejected attempt
		// runs one argon2id verification, so a few tokens refill while the
		// loop runs. 120 attempts leaves ample margin above that.
		const attempts = 120
		firstThrottled := -1
		for i := range attempts {
			body, err := json.Marshal(map[string]string{
				"username": fmt.Sprintf("spray-%d", i),
				"password": "guess",
			})
			Expect(err).NotTo(HaveOccurred())
			req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			spoofed := fmt.Sprintf("198.51.100.%d", i%250+1)
			req.Header.Set("X-Real-IP", spoofed)
			req.Header.Set("X-Forwarded-For", spoofed)
			req.Header.Set("True-Client-IP", spoofed)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code == http.StatusTooManyRequests {
				firstThrottled = i + 1
				break
			}
			Expect(rec.Code).To(Equal(http.StatusUnauthorized),
				"attempt %d: expected a plain credential rejection before the throttle trips; body: %s", i+1, rec.Body.String())
		}

		Expect(firstThrottled).To(BeNumerically(">", 0),
			"%d sign-in attempts from one TCP peer were never per-IP throttled: forwarding headers are choosing the throttle key", attempts)
		Expect(firstThrottled).To(BeNumerically(">", 60),
			"the per-IP bucket (burst 60) must not trip before its burst is spent")
	})
})
