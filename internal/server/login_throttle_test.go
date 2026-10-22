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
// keyed on the client address. That key is only worth anything if a client
// cannot choose it. chi's middleware.RealIP let it: it rewrote r.RemoteAddr
// from X-Real-IP / X-Forwarded-For / True-Client-IP before any handler ran, so
// a client rotating the header got a fresh per-IP bucket on every request and
// could spray passwords across usernames without tripping the per-IP limit.
//
// The client address now comes from the TCP peer, or — only when the peer is
// inside server.trusted_proxies — from X-Forwarded-For, walked right to left
// past trusted hops. These specs pin both halves.
//
// They drive the production route tree (server.NewRouter is the test export of
// newRouter, which New mounts), not the handler, because the bypass lived in
// the middleware chain: a handler-level test cannot see it.
var _ = Describe("local sign-in per-IP throttle through the production router", Ordered, Label("integration"), func() {
	const (
		// httptest.NewRequest's fixed RemoteAddr; never inside a CIDR below.
		untrustedPeer = "192.0.2.1:1234"
		// An ingress pod address inside the trusted CIDR used below.
		trustedPeer = "10.244.1.7:51234"
		trustedCIDR = "10.244.0.0/16"
		// The per-IP bucket holds 60 and refills at 1/s; each rejected attempt
		// runs one argon2id verification, so a few tokens refill while a loop
		// runs. 120 attempts leaves ample margin above that.
		attempts   = 120
		perIPBurst = 60
	)

	var st *store.Store

	BeforeAll(func() {
		ctx := context.Background()
		pg, err := testutil.StartSharedPostgres(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pg.Terminate(context.Background())).To(Succeed()) })
		Expect(store.MigrateUp(ctx, pg.RootURL)).To(Succeed())

		st, err = store.New(ctx, &config.DatabaseConfig{URL: pg.RootURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
	})

	// newRouter builds a fresh production router (and so a fresh, empty
	// throttle) with the given server.trusted_proxies.
	newRouter := func(trustedProxies ...string) http.Handler {
		cfg := &config.Config{
			Server: config.ServerConfig{TrustedProxies: trustedProxies},
			Auth:   config.AuthConfig{InsecureCookies: true},
		}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		authHandler := auth.NewLocalAdmin(cfg, st, logger)
		return server.NewRouter(cfg, st, nil, authHandler, validate.New(&cfg.Validate), server.EmbeddedSchema(), spa.BuildInfo{}, logger, func() bool { return false })
	}

	// login sends one local sign-in attempt for a user that does not exist
	// (so a non-throttled answer is 401) from the given TCP peer, with
	// optional forwarding headers, and returns the status.
	login := func(router http.Handler, peer, username string, headers map[string]string) int {
		body, err := json.Marshal(map[string]string{"username": username, "password": "guess"})
		Expect(err).NotTo(HaveOccurred())
		req := httptest.NewRequest(http.MethodPost, "/api/auth/local/login", bytes.NewReader(body))
		req.RemoteAddr = peer
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		Expect(rec.Code).To(Or(Equal(http.StatusUnauthorized), Equal(http.StatusTooManyRequests)),
			"unexpected status for %s; body: %s", username, rec.Body.String())
		return rec.Code
	}

	// sprayUntilThrottled sends up to `attempts` sign-ins, each with a fresh
	// username (so the per-login bucket, burst 10, never trips and only the
	// per-IP bucket can answer 429), with headers chosen per attempt. It
	// returns the 1-based attempt that was first throttled, or -1.
	sprayUntilThrottled := func(router http.Handler, peer, prefix string, headers func(i int) map[string]string) int {
		for i := range attempts {
			if login(router, peer, fmt.Sprintf("%s-%d", prefix, i), headers(i)) == http.StatusTooManyRequests {
				return i + 1
			}
		}
		return -1
	}

	// Every attempt claims a different client address in all three headers
	// RealIP honoured.
	spoofAll := func(i int) map[string]string {
		spoofed := fmt.Sprintf("198.51.100.%d", i%250+1)
		return map[string]string{"X-Real-IP": spoofed, "X-Forwarded-For": spoofed, "True-Client-IP": spoofed}
	}

	DescribeTable("spoofed forwarding headers from an untrusted peer do not choose the bucket",
		func(trusted []string) {
			first := sprayUntilThrottled(newRouter(trusted...), untrustedPeer, "spray", spoofAll)
			Expect(first).To(BeNumerically(">", 0),
				"%d sign-in attempts from one TCP peer were never per-IP throttled: forwarding headers are choosing the throttle key", attempts)
			Expect(first).To(BeNumerically(">", perIPBurst),
				"the per-IP bucket (burst %d) must not trip before its burst is spent", perIPBurst)
		},
		Entry("trusted_proxies empty (the default)", []string(nil)),
		Entry("trusted_proxies set, peer outside it", []string{trustedCIDR}),
	)

	It("behind a trusted proxy, gives each X-Forwarded-For client its own bucket", func() {
		router := newRouter(trustedCIDR)
		clientA := map[string]string{"X-Forwarded-For": "203.0.113.10"}
		clientB := map[string]string{"X-Forwarded-For": "203.0.113.20"}

		first := sprayUntilThrottled(router, trustedPeer, "a", func(int) map[string]string { return clientA })
		Expect(first).To(BeNumerically(">", perIPBurst), "client A must be throttled on its own bucket, after its burst")

		Expect(login(router, trustedPeer, "b-0", clientB)).To(Equal(http.StatusUnauthorized),
			"client B behind the same ingress must not be locked out by client A exhausting its bucket")
	})

	It("behind a trusted proxy, ignores X-Real-IP and True-Client-IP", func() {
		router := newRouter(trustedCIDR)
		first := sprayUntilThrottled(router, trustedPeer, "hdr", func(i int) map[string]string {
			spoofed := fmt.Sprintf("198.51.100.%d", i%250+1)
			return map[string]string{
				"X-Forwarded-For": "203.0.113.30",
				"X-Real-IP":       spoofed,
				"True-Client-IP":  spoofed,
			}
		})
		Expect(first).To(BeNumerically(">", perIPBurst),
			"only X-Forwarded-For may name the client; rotating X-Real-IP/True-Client-IP must not reset the bucket")
	})

	It("behind a trusted proxy, does not let a client prepend its own X-Forwarded-For entries", func() {
		router := newRouter(trustedCIDR)
		// The client sends "X-Forwarded-For: <rotating fake>, <a trusted-looking
		// address>"; the ingress appends the real peer it saw (203.0.113.40).
		// Walking right to left, the first untrusted entry is the ingress's —
		// the client never gets to pick.
		first := sprayUntilThrottled(router, trustedPeer, "prepend", func(i int) map[string]string {
			return map[string]string{
				"X-Forwarded-For": fmt.Sprintf("198.51.100.%d, 10.244.9.9, 203.0.113.40", i%250+1),
			}
		})
		Expect(first).To(BeNumerically(">", perIPBurst),
			"client-prepended X-Forwarded-For entries must not choose the bucket")
	})
})
