package receiver_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gateway"
	"shepherd/internal/receiver"
	"shepherd/internal/validate"
)

func TestReceiver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Receiver Suite")
}

func readGolden(name string) string {
	GinkgoHelper()
	b, err := os.ReadFile("testdata/" + name + ".golden.alloy")
	Expect(err).NotTo(HaveOccurred(), "golden file testdata/%s.golden.alloy is missing — it must be committed", name)
	return string(b)
}

var _ = Describe("Render", func() {
	DescribeTable("matches the committed golden and passes Stage 1",
		func(name string) {
			out, err := receiver.Render(fixture(name))
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(readGolden(name)), "output mismatch for fixture %s", name)

			r := validate.Stage1(out)
			Expect(r.Valid).To(BeTrue(), "%s fails Stage 1: %+v", name, r.Diagnostics)
		},
		Entry("otlp-basic", "otlp-basic"),
		Entry("otlp-passthrough", "otlp-passthrough"),
		Entry("faro-basic", "faro-basic"),
		Entry("faro-cors-disabled", "faro-cors-disabled"),
		Entry("faro-cors-wildcard", "faro-cors-wildcard"),
		Entry("combined", "combined"),
	)

	It("is deterministic across repeated renders of the same config (map header ordering)", func() {
		cfg := fixture("otlp-basic")
		first, err := receiver.Render(cfg)
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 10; i++ {
			out, err := receiver.Render(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal(first), "render %d differs — likely non-deterministic map iteration", i)
		}
	})

	It("refuses a config with neither OTLP nor Faro pipelines", func() {
		_, err := receiver.Render(receiver.Config{})
		Expect(err).To(HaveOccurred())
	})

	It("refuses an OTLP pipeline with neither HTTP nor GRPC", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label:   "x",
			Metrics: &receiver.OTLPExporter{Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("HTTP or GRPC"))
	})

	It("refuses an OTLP HTTP listener with no MaxRequestBodySize", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			HTTP:  &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("MaxRequestBodySize"))
	})

	It("refuses an OTLP GRPC listener with no MaxConcurrentStreams", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			GRPC:  &receiver.OTLPGRPCListener{ListenAddr: "0.0.0.0:4317", MaxRecvMsgSize: "4MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("MaxConcurrentStreams"))
	})

	It("refuses an OTLP pipeline with no signal wired", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			HTTP:  &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Metrics, Logs, Traces"))
	})

	It("refuses a Faro pipeline with no MaxAllowedPayloadSize", func() {
		_, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label:  "x",
			Server: receiver.FaroServer{ListenAddr: "0.0.0.0", ListenPort: 12347},
			Logs:   &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("MaxAllowedPayloadSize"))
	})

	It("refuses a Faro pipeline with neither Logs nor Traces", func() {
		_, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Logs, Traces"))
	})

	It("refuses a rate limit enabled with no rate/burst set", func() {
		_, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
				RateLimit: receiver.RateLimit{Enabled: true},
			},
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Rate"))
	})

	It("refuses two components of the same type sharing a label", func() {
		otlp := fixture("otlp-basic")
		dup := otlp
		dup.OTLP = append(dup.OTLP, otlp.OTLP[0]) // exact same Label "acme" twice
		_, err := receiver.Render(dup)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate"))
	})

	It("refuses a header set in both Headers and SecretHeaderEnv", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			HTTP:  &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
				Headers:         map[string]string{"Authorization": "literal"},
				SecretHeaderEnv: map[string]string{"Authorization": "ENV_VAR"},
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("both Headers and SecretHeaderEnv"))
	})
})

// ---------------------------------------------------------------------------
// CORS: this receiver, not the gateway, is where docs/gateway-tier-plan.md D2
// requires browser origin enforcement to live (the Gateway API Standard
// channel has no CORS filter at the pinned floor). These tests exist to prove
// that both an empty and a wildcard cors_allowed_origins list can only be
// reached deliberately.
// ---------------------------------------------------------------------------

var _ = Describe("CORS is a deliberate choice, never an accidental default", func() {
	It("the Go zero value renders CORS disabled, not a wildcard", func() {
		var zero receiver.CORSPolicy
		Expect(zero.AllowAll).To(BeFalse())
		Expect(zero.Origins).To(BeEmpty())

		out, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
			CORS: zero,
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`cors_allowed_origins = []`))
		Expect(out).NotTo(ContainSubstring(`"*"`))
	})

	// RED-RUN PROOF: this is the exact one-line change the plan's §8 rule 2
	// asks every control to demonstrate. With validateCORS's "*" check
	// removed (or if a caller reaches around Render and appends "*" to
	// Origins the way any other origin string is appended), this test fails:
	// Render would succeed and emit cors_allowed_origins = ["https://evil.example", "*"]
	// instead of refusing. Confirmed by temporarily commenting out the `if o
	// == "*"` branch in validateCORS: this spec goes from PASS (error
	// returned) to FAIL (Render returns a config containing "*", the
	// Expect(err).To(HaveOccurred()) assertion fails).
	It("a literal \"*\" slipped into Origins is refused, not treated as a normal origin", func() {
		_, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
			CORS: receiver.CORSPolicy{Origins: []string{"https://evil.example", "*"}},
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("wildcard"))
	})

	It("AllowAll renders the wildcard only when set, with Origins empty", func() {
		out, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
			CORS: receiver.CORSPolicy{AllowAll: true},
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`cors_allowed_origins = ["*"]`))
	})

	It("refuses AllowAll combined with a non-empty Origins as ambiguous", func() {
		_, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
			CORS: receiver.CORSPolicy{AllowAll: true, Origins: []string{"https://app.example.com"}},
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).To(HaveOccurred())
	})

	It("an explicit named allowlist renders exactly those origins, nothing more", func() {
		out, err := receiver.Render(receiver.Config{Faro: []receiver.FaroPipeline{{
			Label: "x",
			Server: receiver.FaroServer{
				ListenAddr: "0.0.0.0", ListenPort: 12347, MaxAllowedPayloadSize: "5MiB",
			},
			CORS: receiver.CORSPolicy{Origins: []string{"https://a.example.com", "https://b.example.com"}},
			Logs: &receiver.LokiExporter{Name: "l", URLExpr: `"https://x"`},
		}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`cors_allowed_origins = ["https://a.example.com", "https://b.example.com"]`))
	})
})

// ---------------------------------------------------------------------------
// Rate/size limits: prove they are required inputs, and that the gap Alloy
// cannot fill (OTLP requests/sec throttling) is at least documented in the
// rendered output rather than silently implied.
// ---------------------------------------------------------------------------

var _ = Describe("size and rate limits are explicit, not inherited silently", func() {
	It("renders the OTLP grpc/http size and concurrency caps the caller set", func() {
		out, err := receiver.Render(fixture("otlp-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`max_recv_msg_size      = "4MiB"`))
		Expect(out).To(ContainSubstring(`max_concurrent_streams = 100`))
		Expect(out).To(ContainSubstring(`max_request_body_size  = "8MiB"`))
	})

	It("documents that otelcol.receiver.otlp has no requests/sec limit", func() {
		out, err := receiver.Render(fixture("otlp-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("no requests/sec limit is expressible here"))
	})

	It("renders faro.receiver's payload cap and rate_limiting block", func() {
		out, err := receiver.Render(fixture("faro-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`max_allowed_payload_size = "5MiB"`))
		Expect(out).To(ContainSubstring("rate_limiting {"))
		Expect(out).To(ContainSubstring(`rate       = 50`))
		Expect(out).To(ContainSubstring(`burst_size = 100`))
	})

	It("never emits a secret header as a literal", func() {
		out, err := receiver.Render(fixture("otlp-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring(`sys.env("SHEPHERD_DEST_ACME_LOKI_TOKEN")`))
		Expect(out).To(ContainSubstring(`sys.env("SHEPHERD_DEST_ACME_TEMPO_TOKEN")`))
		Expect(out).NotTo(ContainSubstring("SHEPHERD_DEST_ACME_LOKI_TOKEN\","))
	})
})

// ---------------------------------------------------------------------------
// D10 pass-through tenancy: the failure mode this shape is designed against
// is a pass-through pipeline whose auth is silently misconfigured, shipping
// every tenant's data under one tenant id (or none). These specs prove that
// shape cannot be expressed: every listener and every exporter in a
// TenancyPassThrough pipeline is wired automatically by Render (not a
// per-component opt-in a caller could omit), and the one real ambiguity —
// hardcoding the tenant header AND reading it from context — is refused by
// Validate before Render emits anything.
// ---------------------------------------------------------------------------

var _ = Describe("D10 pass-through tenancy", func() {
	It("static (the zero value) is unaffected: no auth reference, no include_metadata", func() {
		out, err := receiver.Render(fixture("otlp-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).NotTo(ContainSubstring("include_metadata"))
		Expect(out).NotTo(ContainSubstring("otelcol.auth.headers"))
		Expect(out).NotTo(ContainSubstring("auth ="))
	})

	// RED-RUN PROOF: comment out the `if passThrough { ... include_metadata
	// ... }` lines in renderOTLPPipeline (render.go) and this spec's first
	// assertion fails (count goes from 2 to 0) — the golden comparison spec
	// ("otlp-passthrough" in the DescribeTable above) also fails
	// immediately, since testdata/otlp-passthrough.golden.alloy no longer
	// matches. Confirmed 2026-08-21: `go test ./internal/receiver/ -run
	// TestReceiver` reports a golden mismatch on the DescribeTable entry and
	// this spec's `Expect(strings.Count(...)).To(Equal(2))` failing with
	// "Expected <int>: 0\nto equal <int>: 2" once those lines are removed;
	// restoring them returns both specs to green.
	It("wires include_metadata on every configured listener and an auth reference on every configured exporter", func() {
		out, err := receiver.Render(fixture("otlp-passthrough"))
		Expect(err).NotTo(HaveOccurred())

		// Requirement 1: include_metadata set on every listener block the
		// fixture configures. That is exactly one now — Validate refuses a
		// gRPC listener in pass-through mode, since an HTTPRoute cannot front
		// gRPC and the tenant header would be whatever the client sent.
		Expect(strings.Count(out, "include_metadata       = true")).To(Equal(1),
			"expected include_metadata on the http listener (and only that one — pass-through "+
				"has no gRPC listener to set it on)")

		// The shared otelcol.auth.headers component reads the tenant header
		// gateway.RenderHTTPRoute sets (single source of truth — imported,
		// not restated) from context.
		Expect(out).To(ContainSubstring(`otelcol.auth.headers "otlp_shared_tenant"`))
		Expect(out).To(ContainSubstring(fmt.Sprintf("key          = %q", gateway.TenantHeader)))
		Expect(out).To(ContainSubstring(fmt.Sprintf("from_context = %q", strings.ToLower(gateway.TenantHeader))))

		// Requirement 2: every exporter this fixture configures (metrics,
		// logs, traces) references the auth handler — none of them was
		// forgotten.
		Expect(strings.Count(out, "auth = otelcol.auth.headers.otlp_shared_tenant.handler")).To(Equal(3),
			"expected all three exporters (metrics, logs, traces) to reference the auth handler")
	})

	It("refuses a pass-through pipeline whose exporter also hardcodes the tenant header as a literal", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			Mode:  receiver.TenancyPassThrough,
			// HTTP, not gRPC: pass-through refuses a gRPC listener outright,
			// and that refusal would mask the mutual-exclusion check under test.
			HTTP: &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
				Headers: map[string]string{gateway.TenantHeader: "acme"},
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
	})

	It("refuses a pass-through pipeline whose exporter hardcodes the tenant header via SecretHeaderEnv", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			Mode:  receiver.TenancyPassThrough,
			// HTTP, not gRPC: pass-through refuses a gRPC listener outright,
			// and that refusal would mask the mutual-exclusion check under test.
			HTTP: &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
				SecretHeaderEnv: map[string]string{gateway.TenantHeader: "SOME_ENV"},
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
	})

	It("the mutual-exclusion check is case-insensitive, matching HTTP header semantics", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			Mode:  receiver.TenancyPassThrough,
			// HTTP, not gRPC: pass-through refuses a gRPC listener outright,
			// and that refusal would mask the mutual-exclusion check under test.
			HTTP: &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
				Headers: map[string]string{"x-scope-orgid": "acme"},
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
	})

	It("refuses an unrecognized Mode value", func() {
		_, err := receiver.Render(receiver.Config{OTLP: []receiver.OTLPPipeline{{
			Label: "x",
			Mode:  receiver.OTLPTenancyMode("bogus"),
			// HTTP, not gRPC: pass-through refuses a gRPC listener outright,
			// and that refusal would mask the mutual-exclusion check under test.
			HTTP: &receiver.OTLPHTTPListener{ListenAddr: "0.0.0.0:4318", MaxRequestBodySize: "8MiB"},
			Metrics: &receiver.OTLPExporter{
				Name: "m", Protocol: receiver.ExporterGRPC, EndpointExpr: `"https://x"`,
			},
		}}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("mode"))
	})
})

// The defect this pins was found by a final review, not by any test here,
// and it made D10's pass-through mode not work at all while looking correct.
//
// otelcol.processor.batch sits between the receiver and the exporters, and it
// does NOT forward client metadata unless told which keys to preserve. Alloy's
// otelcol.auth.headers reference is explicit: "If an otelcol.processor.batch
// is present in the pipeline, it must be configured to preserve client
// metadata. Do this by adding the value that from_context needs to the
// metadata_keys of the batch processor." Without it, from_context finds
// nothing and omits the header rather than erroring — so EVERY tenant ships
// with no X-Scope-OrgID. Against a strictly multi-tenant destination that is
// an outage; against one with a default tenant it is a silent cross-tenant
// merge, which is the exact outcome D10 exists to prevent.
//
// Nothing already here could catch it: the config is syntactically valid, so
// `alloy validate` passes, and the kind suite's backend is an echo server
// rather than a real Alloy pipeline. That is §10's "alloy validate is not
// alloy run" one layer up, so this asserts the WIRING invariant instead —
// tenancy read from context at the exporter is only real if every stage
// between it and the listener preserves the key.
//
// Red run, executed: dropping the `if passThrough` metadata_keys block in
// renderBatch fails this spec with `a pass-through pipeline batches through
// otelcol.processor.batch without preserving "x-scope-orgid"`.
var _ = Describe("pass-through tenancy survives the batch processor", func() {
	It("preserves the tenant key on every batch processor in a pass-through pipeline", func() {
		out, err := receiver.Render(fixture("otlp-passthrough"))
		Expect(err).NotTo(HaveOccurred())

		Expect(out).To(ContainSubstring("otelcol.processor.batch"),
			"fixture no longer batches — this spec would pass vacuously")
		Expect(out).To(ContainSubstring(`metadata_keys = ["x-scope-orgid"]`),
			"a pass-through pipeline batches through otelcol.processor.batch without preserving "+
				"%q, so from_context finds nothing at the exporter and every tenant ships untagged",
			strings.ToLower(gateway.TenantHeader))

		// The key preserved must be the same one the auth component reads.
		// Two independently-written literals here is exactly how this drifts.
		Expect(out).To(ContainSubstring(`from_context = "x-scope-orgid"`))
	})

	It("does not add metadata_keys to a static-tenancy pipeline", func() {
		out, err := receiver.Render(fixture("otlp-basic"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out).NotTo(ContainSubstring("metadata_keys"),
			"static tenancy hardcodes the tenant per pipeline and reads nothing from context; "+
				"preserving metadata there would shard batches for no reason")
	})
})

// Red run, executed: deleting the `p.Mode == TenancyPassThrough && p.GRPC != nil`
// refusal in validateOTLPPipeline fails this spec with `Render accepted a
// pass-through pipeline with a gRPC listener`.
var _ = Describe("pass-through refuses a listener no rendered route can front", func() {
	It("refuses a gRPC listener in pass-through mode", func() {
		cfg := fixture("otlp-passthrough")
		cfg.OTLP[0].GRPC = &receiver.OTLPGRPCListener{
			ListenAddr: "0.0.0.0:4317", MaxRecvMsgSize: "4MiB", MaxConcurrentStreams: 200,
		}
		_, err := receiver.Render(cfg)
		Expect(err).To(HaveOccurred(),
			"Render accepted a pass-through pipeline with a gRPC listener — an HTTPRoute cannot "+
				"front gRPC, so every client would choose its own tenant")
		Expect(err.Error()).To(ContainSubstring("GRPCRoute"),
			"the refusal must say why, not just that it refused")
	})

	It("still allows a gRPC listener under static tenancy", func() {
		cfg := fixture("otlp-basic")
		Expect(cfg.OTLP[0].GRPC).NotTo(BeNil(), "fixture no longer has gRPC — spec would pass vacuously")
		_, err := receiver.Render(cfg)
		Expect(err).NotTo(HaveOccurred(),
			"static tenancy hardcodes the tenant per pipeline, so gRPC needs no gateway-set header")
	})
})
