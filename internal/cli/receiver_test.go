package cli

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A pass-through receiver the chart would write: one OTLP/HTTP listener,
// traces to a literal endpoint, metrics to an env-provided one with its
// credential by env reference.
const validReceiverFile = `
otlp:
  - label: tenants
    mode: pass_through
    http:
      listen_addr: "0.0.0.0:4318"
      max_request_body_size: "8MiB"
    batch:
      timeout: "5s"
      send_batch_size: 8192
    traces:
      name: tempo
      protocol: http
      endpoint: "https://tempo.example.internal:4318"
    metrics:
      name: mimir
      protocol: http
      endpoint_env: RECEIVER_MIMIR_URL
      secret_header_env:
        Authorization: RECEIVER_MIMIR_AUTH
`

var _ = Describe("shepherd receiver render", func() {
	writeFile := func(content string) string {
		p := filepath.Join(GinkgoT().TempDir(), "receiver.yaml")
		Expect(os.WriteFile(p, []byte(content), 0o600)).To(Succeed())
		return p
	}

	It("renders a pass-through receiver with its tenancy wiring and env-referenced secrets", func() {
		out, err := renderReceiverFile(writeFile(validReceiverFile))
		Expect(err).NotTo(HaveOccurred())
		// The three places pass-through tenancy lives (gateway plan D10):
		// the listener keeps client metadata, the batch processor forwards
		// the tenant key (the 2026-08-22 defect), every exporter reads it.
		Expect(out).To(ContainSubstring("include_metadata"))
		Expect(out).To(ContainSubstring(`metadata_keys = ["x-scope-orgid"]`))
		Expect(out).To(ContainSubstring("from_context"))
		Expect(out).To(ContainSubstring(`"https://tempo.example.internal:4318"`))
		Expect(out).To(ContainSubstring(`sys.env("RECEIVER_MIMIR_URL")`))
		Expect(out).To(ContainSubstring(`sys.env("RECEIVER_MIMIR_AUTH")`))
		Expect(out).NotTo(ContainSubstring("grpc"))
	})

	DescribeTable("refuses a config it cannot render safely",
		func(content, wantErr string) {
			_, err := renderReceiverFile(writeFile(content))
			Expect(err).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("an unknown key", validReceiverFile+"extra: true\n", "field extra not found"),
		Entry("a gRPC listener (pass-through cannot front it)",
			`
otlp:
  - label: t
    mode: pass_through
    grpc: {listen_addr: "0.0.0.0:4317"}
`, "field grpc not found"),
		Entry("a missing mode", `
otlp:
  - label: t
    http: {listen_addr: "0.0.0.0:4318", max_request_body_size: "8MiB"}
    traces: {name: tempo, protocol: http, endpoint: "https://tempo:4318"}
`, `mode must be "pass_through" or "static"`),
		Entry("both endpoint and endpoint_env", `
otlp:
  - label: t
    mode: pass_through
    http: {listen_addr: "0.0.0.0:4318", max_request_body_size: "8MiB"}
    traces: {name: tempo, protocol: http, endpoint: "https://tempo:4318", endpoint_env: TEMPO_URL}
`, "not both"),
		Entry("an endpoint that is an Alloy expression, not a URL", `
otlp:
  - label: t
    mode: pass_through
    http: {listen_addr: "0.0.0.0:4318", max_request_body_size: "8MiB"}
    traces: {name: tempo, protocol: http, endpoint: 'sys.env("X")'}
`, "must be an absolute http(s) URL"),
		Entry("an endpoint_env that is not an env var name", `
otlp:
  - label: t
    mode: pass_through
    http: {listen_addr: "0.0.0.0:4318", max_request_body_size: "8MiB"}
    traces: {name: tempo, protocol: http, endpoint_env: 'x") + sys.env("Y'}
`, "must be an environment variable name"),
		Entry("a secret header whose value is not an env var name", `
otlp:
  - label: t
    mode: pass_through
    http: {listen_addr: "0.0.0.0:4318", max_request_body_size: "8MiB"}
    traces: {name: tempo, protocol: http, endpoint: "https://tempo:4318", secret_header_env: {Authorization: "Bearer abc"}}
`, "must be an environment variable name"),
		Entry("a listener without a body-size limit (receiver.Validate)", `
otlp:
  - label: t
    mode: pass_through
    http: {listen_addr: "0.0.0.0:4318"}
    traces: {name: tempo, protocol: http, endpoint: "https://tempo:4318"}
`, "MaxRequestBodySize must be set explicitly"),
		Entry("an empty file", "", "empty receiver config"),
	)

	It("prints the refusal without cobra's usage text, so the pod log shows only the reason", func() {
		// Other specs (token_test.go) set SilenceErrors/SilenceUsage on the
		// shared rootCmd; pin them to cobra's defaults so this spec tests the
		// render command's OWN SilenceUsage, whatever ran before it.
		prevErrs, prevUsage := rootCmd.SilenceErrors, rootCmd.SilenceUsage
		rootCmd.SilenceErrors, rootCmd.SilenceUsage = false, false
		defer func() { rootCmd.SilenceErrors, rootCmd.SilenceUsage = prevErrs, prevUsage }()
		// cobra writes the error to Err but the usage text to Out: capture both.
		var errOut bytes.Buffer
		rootCmd.SetErr(&errOut)
		rootCmd.SetOut(&errOut)
		defer func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) }()
		rootCmd.SetArgs([]string{
			"receiver", "render",
			"--config", writeFile(validReceiverFile + "extra: true\n"), "--out", filepath.Join(GinkgoT().TempDir(), "x.alloy"),
		})
		Expect(rootCmd.Execute()).To(HaveOccurred())
		Expect(errOut.String()).To(ContainSubstring("field extra not found"))
		Expect(errOut.String()).NotTo(ContainSubstring("Usage:"))
	})

	It("writes nothing when validation fails, so the pod fails at init", func() {
		dir := GinkgoT().TempDir()
		out := filepath.Join(dir, "config.alloy")
		rootCmd.SetArgs([]string{
			"receiver", "render",
			"--config", writeFile(validReceiverFile + "extra: true\n"), "--out", out,
		})
		Expect(rootCmd.Execute()).To(HaveOccurred())
		Expect(out).NotTo(BeAnExistingFile())

		rootCmd.SetArgs([]string{"receiver", "render", "--config", writeFile(validReceiverFile), "--out", out})
		Expect(rootCmd.Execute()).To(Succeed())
		Expect(out).To(BeAnExistingFile())
	})
})
