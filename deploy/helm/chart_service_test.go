package helm_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// renderServiceWith runs the real `helm template` over the chart with the given
// extra values and returns the Service's first port as a map — the same
// "read it back out of ACTUAL helm output" discipline chart_test.go uses.
func renderServiceWith(extraValues string) map[string]any {
	dir := GinkgoT().TempDir()
	overridePath := filepath.Join(dir, "override.yaml")
	Expect(os.WriteFile(overridePath, []byte(extraValues), 0o600)).To(Succeed())
	cmd := exec.Command("helm", "template", "shepherd", "shepherd",
		"-f", "shepherd/ci/default-values.yaml",
		"-f", overridePath,
	)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "helm template failed:\n%s", out)
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc map[string]any
		if decErr := dec.Decode(&doc); decErr != nil {
			break
		}
		if doc["kind"] != "Service" {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any) //nolint:errcheck // rendered by helm, shape is known
		if meta["name"] != "shepherd" {
			continue
		}
		spec, _ := doc["spec"].(map[string]any) //nolint:errcheck // same
		ports, _ := spec["ports"].([]any)       //nolint:errcheck // same
		port, _ := ports[0].(map[string]any)    //nolint:errcheck // same
		return port
	}
	Fail("Service/shepherd not rendered")
	return nil
}

// The walkthrough of the kind dev stack (2026-09-14) found that the chart's
// own HTTPRoute could never resolve through NGINX Gateway Fabric: the Service
// hardcoded appProtocol kubernetes.io/h2c and NGF refuses to proxy an HTTP
// route to an h2c upstream ("UnsupportedProtocol"). Shepherd also serves
// HTTP/1.1, so the hint is optional; it stays the default for gateways that
// use it, and clearing it must drop the field entirely.
var _ = Describe("Helm chart: Service appProtocol (kind dev stack finding)", func() {
	It("keeps kubernetes.io/h2c as the default", func() {
		port := renderServiceWith("")
		Expect(port["appProtocol"]).To(Equal("kubernetes.io/h2c"))
	})

	It("omits appProtocol entirely when service.appProtocol is empty", func() {
		port := renderServiceWith("service:\n  appProtocol: \"\"\n")
		Expect(port).NotTo(HaveKey("appProtocol"))
	})
})
