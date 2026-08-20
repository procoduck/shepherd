package simsvc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
)

var _ = Describe("Service account token guard", func() {
	// §6.4 lists "no service account token" first among the containment
	// controls. automountServiceAccountToken: false is exactly the kind of pod
	// spec key that gets dropped in a values file with nothing noticing, so the
	// control is expressed as a refusal to boot: loud, and testable without a
	// cluster.
	It("refuses to start when a token is mounted", func() {
		dir := GinkgoT().TempDir()
		token := filepath.Join(dir, "token")
		Expect(os.WriteFile(token, []byte("not-a-real-token"), 0o600)).To(Succeed())

		err := CheckServiceAccountToken(token)
		Expect(err).To(MatchError(ErrServiceAccountToken))
		Expect(err.Error()).To(ContainSubstring("refusing to start"))
		Expect(err.Error()).To(ContainSubstring(token))
	})

	It("starts when no token is mounted", func() {
		Expect(CheckServiceAccountToken(filepath.Join(GinkgoT().TempDir(), "absent"))).To(Succeed())
	})

	It("refuses through Serve, before any listener is bound", func() {
		dir := GinkgoT().TempDir()
		token := filepath.Join(dir, "token")
		Expect(os.WriteFile(token, []byte("not-a-real-token"), 0o600)).To(Succeed())

		cfg := testConfig(dir)
		cfg.SATokenPath = token
		// Port 1 is privileged and would fail to bind; Serve returning the
		// token error rather than a bind error is the proof the guard runs
		// first.
		cfg.Listen = "127.0.0.1:1"
		cfg.CaptureListen = "127.0.0.1:1"

		Expect(Serve(GinkgoT().Context(), cfg, nil)).To(MatchError(ErrServiceAccountToken))
	})
})

var _ = Describe("Endpoint allowlist", func() {
	const allowed = "shepherd-simulator"

	It("rejects a config naming a host the harness does not serve", func() {
		config := fmt.Sprintf(`
prometheus.remote_write "leak" {
  endpoint {
    url = %q
  }
}
`, "https://metrics.example.com/api/v1/write")

		err := CheckEndpoints(config, []string{allowed, "127.0.0.1"})
		Expect(err).To(MatchError(ErrEndpointNotAllowed))
		Expect(err.Error()).To(ContainSubstring("metrics.example.com"))
	})

	It("rejects an httptest server URL, so a run cannot be pointed at a listener inside the test process", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(server.Close)

		config := fmt.Sprintf(`
prometheus.remote_write "leak" {
  endpoint {
    url = %q
  }
}
`, server.URL+simulate.CapturePathPrometheus)

		Expect(CheckEndpoints(config, []string{allowed})).To(MatchError(ErrEndpointNotAllowed))
	})

	It("accepts a config whose destinations all point at the harness", func() {
		config := fmt.Sprintf(`
prometheus.remote_write "cap" {
  endpoint {
    url = %q
  }
}

loki.write "cap" {
  endpoint {
    url = %q
  }
}

otelcol.exporter.otlp "cap" {
  client {
    endpoint = %q
    tls {
      insecure = true
    }
  }
}
`,
			"http://"+allowed+":9110"+simulate.CapturePathPrometheus,
			"http://"+allowed+":9110"+simulate.CapturePathLoki,
			allowed+":4317",
		)

		Expect(CheckEndpoints(config, []string{allowed})).To(Succeed())
	})

	It("rejects a bare host:port outside the allowlist", func() {
		config := `
otelcol.exporter.otlp "leak" {
  client {
    endpoint = "otel.example.com:4317"
  }
}
`
		err := CheckEndpoints(config, []string{allowed})
		Expect(err).To(MatchError(ErrEndpointNotAllowed))
		Expect(err.Error()).To(ContainSubstring("otel.example.com"))
	})

	It("reports a config that does not parse rather than passing it through", func() {
		Expect(CheckEndpoints(`prometheus.remote_write "cap" {`, []string{allowed})).
			To(MatchError(ContainSubstring("invalid_config")))
	})
})
