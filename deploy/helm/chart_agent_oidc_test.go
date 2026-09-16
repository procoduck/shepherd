package helm_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// renderConfigYaml runs the real `helm template` with the given override and
// returns the server config the ConfigMap carries (shepherd.yaml), parsed.
func renderConfigYaml(extraValues string) map[string]any {
	dir := GinkgoT().TempDir()
	overridePath := filepath.Join(dir, "override.yaml")
	Expect(os.WriteFile(overridePath, []byte(extraValues), 0o600)).To(Succeed())
	cmd := exec.Command("helm", "template", "shepherd", "shepherd",
		"-f", "shepherd/ci/default-values.yaml", "-f", overridePath)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "helm template failed:\n%s", out)

	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc map[string]any
		if decErr := dec.Decode(&doc); decErr != nil {
			break
		}
		if doc["kind"] != "ConfigMap" {
			continue
		}
		data, _ := doc["data"].(map[string]any) //nolint:errcheck // helm output, shape known
		raw, _ := data["shepherd.yaml"].(string) //nolint:errcheck // same
		if raw == "" {
			continue
		}
		var cfg map[string]any
		Expect(yaml.Unmarshal([]byte(raw), &cfg)).To(Succeed())
		return cfg
	}
	Fail("ConfigMap shepherd.yaml not rendered")
	return nil
}

// Collector-OIDC values (docs/plans/2026-09-16-agent-oidc-auth.md, Phase 3):
// the chart must let an operator set the agent-OIDC and beacon-auth keys, and
// they must reach the server config. values.schema.json has
// additionalProperties:false, so an unlisted key would make `helm template`
// fail — this spec is exactly what proves the schema was extended.
var _ = Describe("collector OIDC chart values", func() {
	It("passes the agent-OIDC and beacon keys through to the server config", func() {
		cfg := renderConfigYaml(`
config:
  oidc:
    issuer: "https://idp.example/"
    client_id: "shepherd-login"
    agent_audience: "api://shepherd-collectors"
    agent_required_role: "Collector.Poll"
    beacon_auth: "oauth2"
    agent_token_url: "https://idp.example/token"
    agent_scopes: ["api://shepherd-collectors/.default"]
`)
		oidc, ok := cfg["oidc"].(map[string]any)
		Expect(ok).To(BeTrue(), "config.oidc must be present")
		Expect(oidc["agent_audience"]).To(Equal("api://shepherd-collectors"))
		Expect(oidc["agent_required_role"]).To(Equal("Collector.Poll"))
		Expect(oidc["beacon_auth"]).To(Equal("oauth2"))
		Expect(oidc["agent_token_url"]).To(Equal("https://idp.example/token"))
	})

	It("rejects an invalid beacon_auth via the values schema", func() {
		dir := GinkgoT().TempDir()
		overridePath := filepath.Join(dir, "override.yaml")
		Expect(os.WriteFile(overridePath, []byte("config:\n  oidc:\n    beacon_auth: \"sometimes\"\n"), 0o600)).To(Succeed())
		cmd := exec.Command("helm", "template", "shepherd", "shepherd",
			"-f", "shepherd/ci/default-values.yaml", "-f", overridePath)
		out, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred(), "the schema enum must reject an unknown beacon_auth")
		Expect(strings.ToLower(string(out))).To(ContainSubstring("beacon_auth"))
	})
})
