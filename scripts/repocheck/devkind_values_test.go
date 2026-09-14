package repocheck_test

import (
	"bytes"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// asMap and asSlice check the type-assertion's ok result explicitly (rather
// than discarding it into "_", which errcheck's check-type-assertions rule
// flags) and fail the spec with a useful message when a rendered object's
// shape does not match what the test expects.
func asMap(v any) map[string]any {
	GinkgoHelper()
	m, ok := v.(map[string]any)
	Expect(ok).To(BeTrue(), "expected a map, got %T: %#v", v, v)
	return m
}

func asSlice(v any) []any {
	GinkgoHelper()
	s, ok := v.([]any)
	Expect(ok).To(BeTrue(), "expected a slice, got %T: %#v", v, v)
	return s
}

// renderDevKindValues runs the real `helm template` binary (not a hand-parsed
// approximation of it) over deploy/helm/shepherd with dev/kind/values.yaml
// layered on top, the same invocation scripts/dev-kind.sh's `up` verb uses
// (plan §1's Chart values row), and returns every rendered object keyed by
// "Kind/Name". A control this spec does not read back out of ACTUAL helm
// output could pass while the values file it is meant to guard is broken.
func renderDevKindValues() map[string]map[string]any {
	GinkgoHelper()
	root := repoRoot()
	cmd := exec.Command("helm", "template", "shepherd",
		filepath.Join(root, "deploy", "helm", "shepherd"),
		"-f", filepath.Join(root, "dev", "kind", "values.yaml"),
		"--namespace", "shepherd-dev")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred(), "helm template failed:\n%s", stderr.String())

	objects := map[string]map[string]any{}
	dec := yaml.NewDecoder(bytes.NewReader(out.Bytes()))
	for {
		var doc map[string]any
		if decErr := dec.Decode(&doc); decErr != nil {
			break
		}
		if doc == nil {
			continue
		}
		// A document missing "kind" or "metadata.name" is not a k8s object
		// worth indexing (e.g. NOTES.txt has neither) -- ok deliberately
		// unchecked and defaulted to the zero value via the comma-ok form.
		kind, kindOK := doc["kind"].(string)
		if !kindOK {
			continue
		}
		meta, metaOK := doc["metadata"].(map[string]any)
		if !metaOK {
			continue
		}
		name, nameOK := meta["name"].(string)
		if !nameOK || kind == "" || name == "" {
			continue
		}
		objects[kind+"/"+name] = doc
	}
	return objects
}

// firstContainer digs spec.template.spec.containers[0] out of a rendered
// Deployment document.
func firstContainer(doc map[string]any) map[string]any {
	GinkgoHelper()
	spec := asMap(doc["spec"])
	tmpl := asMap(spec["template"])
	podSpec := asMap(tmpl["spec"])
	containers := asSlice(podSpec["containers"])
	Expect(containers).NotTo(BeEmpty(), "Deployment has no containers")
	return asMap(containers[0])
}

// Red run, 2026-09-14: dev/kind/values.yaml does not exist yet, so
// `helm template shepherd deploy/helm/shepherd -f dev/kind/values.yaml
// --namespace shepherd-dev` failed with:
//
//	Error: open dev/kind/values.yaml: no such file or directory
//	helm.go:81: [debug] open dev/kind/values.yaml: no such file or directory
//
// (run from the repo root, 2026-09-14). Every It below failed the same way,
// via renderDevKindValues's Expect(err).NotTo(HaveOccurred()).
var _ = Describe("dev/kind/values.yaml", func() {
	var objects map[string]map[string]any

	BeforeEach(func() {
		objects = renderDevKindValues()
	})

	It("runs Shepherd as a single replica pulling shepherd:local, never from a registry", func() {
		dep := objects["Deployment/shepherd"]
		Expect(dep).NotTo(BeNil(), "no Deployment/shepherd in the render")

		spec := asMap(dep["spec"])
		Expect(spec["replicas"]).To(BeNumerically("==", 1))

		c := firstContainer(dep)
		Expect(c["image"]).To(Equal("shepherd:local"))
		Expect(c["imagePullPolicy"]).To(Equal("Never"))

		envFrom := asSlice(c["envFrom"])
		Expect(envFrom).NotTo(BeEmpty(), "container has no envFrom")
		secretRef := asMap(asMap(envFrom[0])["secretRef"])
		Expect(secretRef["name"]).To(Equal("shepherd-dev-env"),
			"existingSecret must be wired through envFrom, not the chart's own generated Secret")

		var dbURL map[string]any
		for _, e := range asSlice(c["env"]) {
			entry := asMap(e)
			if entry["name"] == "SHEPHERD_DATABASE_URL" {
				dbURL = entry
			}
		}
		Expect(dbURL).NotTo(BeNil(), "SHEPHERD_DATABASE_URL must be wired from the CNPG cluster's secret (cnpg.enabled: true)")
		secretKeyRef := asMap(asMap(dbURL["valueFrom"])["secretKeyRef"])
		Expect(secretKeyRef["name"]).To(Equal("shepherd-db-app"))
		Expect(secretKeyRef["key"]).To(Equal("uri"))
	})

	It("runs the S3 sandbox simulator pulling shepherd-simulator:local, never from a registry", func() {
		dep := objects["Deployment/shepherd-simulator"]
		Expect(dep).NotTo(BeNil(), "no Deployment/shepherd-simulator — simulator.enabled must stay on (the chart default) for Calico's containment policy to be real in dev")

		c := firstContainer(dep)
		Expect(c["image"]).To(Equal("shepherd-simulator:local"))
		Expect(c["imagePullPolicy"]).To(Equal("Never"))
	})

	It("routes shepherd.localtest.me to the shepherd Service through the dev Gateway", func() {
		route := objects["HTTPRoute/shepherd"]
		Expect(route).NotTo(BeNil(), "no HTTPRoute/shepherd in the render")

		spec := asMap(route["spec"])
		hostnames := asSlice(spec["hostnames"])
		Expect(hostnames).To(ConsistOf("shepherd.localtest.me"))

		parentRefs := asSlice(spec["parentRefs"])
		Expect(parentRefs).To(HaveLen(1))
		Expect(asMap(parentRefs[0])["name"]).To(Equal("dev"))

		rules := asSlice(spec["rules"])
		Expect(rules).To(HaveLen(1))
		backendRefs := asSlice(asMap(rules[0])["backendRefs"])
		Expect(backendRefs).To(HaveLen(1))
		backendRef := asMap(backendRefs[0])
		Expect(backendRef["name"]).To(Equal("shepherd"))
		Expect(backendRef["port"]).To(BeNumerically("==", 8080))
	})

	It("provisions a single-instance CloudNativePG cluster with 1Gi storage", func() {
		cluster := objects["Cluster/shepherd-db"]
		Expect(cluster).NotTo(BeNil(), "no Cluster/shepherd-db — cnpg.enabled must be true")

		spec := asMap(cluster["spec"])
		Expect(spec["instances"]).To(BeNumerically("==", 1))
		storage := asMap(spec["storage"])
		Expect(storage["size"]).To(Equal("1Gi"))
	})

	It("renders no chart-generated Secret — existingSecret must be the only Secret in play", func() {
		Expect(objects).NotTo(HaveKey("Secret/shepherd-secrets"),
			"a Secret/shepherd-secrets rendering means .Values.secrets is non-empty even though "+
				"existingSecret is set, contradicting the values file's own comment about which one wins")
	})

	It("declares the mock OIDC issuer and Shepherd's own base_url in the rendered app config", func() {
		cm := objects["ConfigMap/shepherd"]
		Expect(cm).NotTo(BeNil(), "no ConfigMap/shepherd in the render")
		data := asMap(cm["data"])
		shepherdYAML, ok := data["shepherd.yaml"].(string)
		Expect(ok).To(BeTrue(), "ConfigMap/shepherd has no string data[shepherd.yaml]")
		Expect(shepherdYAML).NotTo(BeEmpty())

		var cfg map[string]any
		Expect(yaml.Unmarshal([]byte(shepherdYAML), &cfg)).To(Succeed())

		server := asMap(cfg["server"])
		Expect(server["base_url"]).To(Equal("http://shepherd.localtest.me"))

		oidc := asMap(cfg["oidc"])
		Expect(oidc["provider"]).To(Equal("generic"))
		Expect(oidc["display_name"]).To(Equal("Mock SSO"))
		Expect(oidc["issuer"]).To(Equal("http://oidc.localtest.me/default"))
		Expect(oidc["client_id"]).To(Equal("shepherd-dev"))
		Expect(oidc["redirect_url"]).To(Equal("http://shepherd.localtest.me/auth/callback"))
	})

	It("passes helm lint --strict", func() {
		root := repoRoot()
		cmd := exec.Command("helm", "lint", "--strict",
			filepath.Join(root, "deploy", "helm", "shepherd"),
			"-f", filepath.Join(root, "dev", "kind", "values.yaml"))
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "helm lint --strict failed:\n%s", out)
	})
})
