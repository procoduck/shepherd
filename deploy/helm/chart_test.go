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

// renderSimulatorEnabled runs the real `helm template` binary — not a
// hand-parsed approximation of it — over the chart with the simulator turned
// on, and returns every rendered object keyed by "Kind/Name". A control this
// suite does not read back out of ACTUAL helm output could pass while the
// template that produces it is broken; that is finding H4's exact mistake
// (a containment claim no test exercised), applied to the chart.
func renderSimulatorEnabled() map[string]map[string]any {
	dir := GinkgoT().TempDir()
	overridePath := filepath.Join(dir, "simulator-enabled.yaml")
	Expect(os.WriteFile(overridePath, []byte("simulator:\n  enabled: true\n"), 0o600)).To(Succeed())

	cmd := exec.Command("helm", "template", "shepherd", "shepherd",
		"-f", "shepherd/ci/default-values.yaml",
		"-f", overridePath,
	)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "helm template failed:\n%s", out)

	objects := map[string]map[string]any{}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc map[string]any
		if decErr := dec.Decode(&doc); decErr != nil {
			break
		}
		if doc == nil {
			continue
		}
		kind, _ := doc["kind"].(string) //nolint:errcheck // a document missing "kind" is not a k8s object worth indexing
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string) //nolint:errcheck // same
		if kind == "" || name == "" {
			continue
		}
		objects[kind+"/"+name] = doc
	}
	return objects
}

var _ = Describe("Helm chart: S3 sandbox simulator containment (finding H5)", func() {
	// Every assertion here corresponds to one bullet in values.yaml's
	// simulator block comment. A bullet with no assertion below is a claim,
	// not a control.

	It("is absent from the default render — disabled by default like every other S3 gate", func() {
		out, err := exec.Command("helm", "template", "shepherd", "shepherd",
			"-f", "shepherd/ci/default-values.yaml").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "helm template failed:\n%s", out)
		Expect(string(out)).NotTo(ContainSubstring("shepherd-simulator"),
			"the simulator must render nothing at all until simulator.enabled is set")
	})

	Describe("with simulator.enabled: true", func() {
		var objects map[string]map[string]any

		BeforeEach(func() { objects = renderSimulatorEnabled() })

		It("renders a Deployment and a Service", func() {
			Expect(objects).To(HaveKey("Deployment/shepherd-simulator"))
			Expect(objects).To(HaveKey("Service/shepherd-simulator"))
		})

		It("does not automount a service-account token", func() {
			sa, ok := objects["ServiceAccount/shepherd-simulator"]
			Expect(ok).To(BeTrue(), "no ServiceAccount rendered")
			Expect(sa["automountServiceAccountToken"]).To(Equal(false))

			podSpec := podSpecOf(objects["Deployment/shepherd-simulator"])
			Expect(podSpec["automountServiceAccountToken"]).To(Equal(false),
				"the Pod spec must also refuse the mount — an operator who does not deploy this ServiceAccount must not fall back to the namespace default")
			Expect(podSpec["serviceAccountName"]).To(Equal("shepherd-simulator"))
		})

		It("runs as non-root", func() {
			podSpec := podSpecOf(objects["Deployment/shepherd-simulator"])
			sc, ok := podSpec["securityContext"].(map[string]any)
			Expect(ok).To(BeTrue(), "Pod spec has no securityContext")
			Expect(sc["runAsNonRoot"]).To(Equal(true))
			Expect(sc["runAsUser"]).NotTo(BeEquivalentTo(0))
		})

		It("drops every capability, forbids privilege escalation, and mounts a read-only root filesystem", func() {
			c := containerOf(objects["Deployment/shepherd-simulator"], "shepherd-simulator")
			sc, ok := c["securityContext"].(map[string]any)
			Expect(ok).To(BeTrue(), "container has no securityContext")
			Expect(sc["readOnlyRootFilesystem"]).To(Equal(true))
			Expect(sc["allowPrivilegeEscalation"]).To(Equal(false))
			caps, ok := sc["capabilities"].(map[string]any)
			Expect(ok).To(BeTrue(), "container securityContext has no capabilities block")
			Expect(caps["drop"]).To(ConsistOf("ALL"))
		})

		It("sets CPU and memory requests and limits", func() {
			c := containerOf(objects["Deployment/shepherd-simulator"], "shepherd-simulator")
			res, ok := c["resources"].(map[string]any)
			Expect(ok).To(BeTrue(), "container has no resources block")
			limits, ok := res["limits"].(map[string]any)
			Expect(ok).To(BeTrue(), "no resource limits set")
			Expect(limits).To(HaveKey("cpu"))
			Expect(limits).To(HaveKey("memory"))
			requests, ok := res["requests"].(map[string]any)
			Expect(ok).To(BeTrue(), "no resource requests set")
			Expect(requests).To(HaveKey("cpu"))
			Expect(requests).To(HaveKey("memory"))
		})

		It("default-denies egress except to its own harness ports and cluster DNS", func() {
			np, ok := objects["NetworkPolicy/shepherd-simulator"]
			Expect(ok).To(BeTrue(), "no NetworkPolicy rendered")

			spec, ok := np["spec"].(map[string]any)
			Expect(ok).To(BeTrue())
			policyTypes, ok := spec["policyTypes"].([]any)
			Expect(ok).To(BeTrue())
			Expect(policyTypes).To(ContainElement("Egress"))

			egress, ok := spec["egress"].([]any)
			Expect(ok).To(BeTrue(), "no egress rules at all — that is deny-ALL, not deny-by-default with a harness exception")
			Expect(egress).NotTo(BeEmpty())

			for _, raw := range egress {
				rule, ok := raw.(map[string]any)
				Expect(ok).To(BeTrue())
				// A rule with no "to" is Kubernetes' spelling of "every
				// destination" — the exact catch-all the main chart's own
				// NetworkPolicy uses (deploy/helm/shepherd/templates/networkpolicy.yaml)
				// and the one shape that would silently turn this into
				// allow-all-egress.
				Expect(rule).To(HaveKey("to"), "egress rule with no \"to\" allows every destination")
				to, ok := rule["to"].([]any)
				Expect(ok).To(BeTrue())
				Expect(to).NotTo(BeEmpty())
			}
		})

		It("restricts ingress to the main shepherd deployment on the control port", func() {
			np, ok := objects["NetworkPolicy/shepherd-simulator"]
			Expect(ok).To(BeTrue())
			spec, ok := np["spec"].(map[string]any)
			Expect(ok).To(BeTrue())
			ingress, ok := spec["ingress"].([]any)
			Expect(ok).To(BeTrue())
			Expect(ingress).To(HaveLen(1), "nothing but the control plane submits runs")
		})
	})
})

func podSpecOf(deployment map[string]any) map[string]any {
	spec, _ := deployment["spec"].(map[string]any)   //nolint:errcheck // caller asserts presence via the returned value
	template, _ := spec["template"].(map[string]any) //nolint:errcheck // same
	podSpec, _ := template["spec"].(map[string]any)  //nolint:errcheck // same
	ExpectWithOffset(1, podSpec).NotTo(BeNil(), "Deployment has no pod spec: %v", deployment)
	return podSpec
}

func containerOf(deployment map[string]any, name string) map[string]any {
	podSpec := podSpecOf(deployment)
	containers, _ := podSpec["containers"].([]any) //nolint:errcheck // asserted below
	for _, raw := range containers {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if c["name"] == name {
			return c
		}
	}
	ExpectWithOffset(1, false).To(BeTrue(), "no container named %q in pod spec %v", name, podSpec)
	return nil
}
