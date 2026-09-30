package helm_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// Receiver tier, PR 2 of docs/plans/2026-09-28-receiver-tier.md: the chart's
// receiver objects, off by default, and the posture review gate R3 asks for.
// The runtime half (real Alloy, real gateway, Calico-enforced policy) is the
// kind suite's; these pin what the chart renders.

// receiverOn is the smallest values override that turns the receiver on: both
// NetworkPolicy lists are required, and so is one exporter.
const receiverOn = `
receiver:
  enabled: true
  exporters:
    traces: {enabled: true, endpoint: "https://tempo.example.internal:4318"}
    metrics:
      enabled: true
      endpointFromSecret: {name: mimir-endpoint}
      authorizationFromSecret: {name: mimir-auth}
  networkPolicy:
    gatewayFrom:
      - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: gateway}}
        podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: edge}}
    egress:
      - to: [{ipBlock: {cidr: 10.0.0.0/8}}]
`

// helmTemplate renders the chart with the CI defaults plus an override and
// returns every object, or the command's output as the error text.
func helmTemplate(override string) ([]map[string]any, string, error) {
	dir := GinkgoT().TempDir()
	p := filepath.Join(dir, "override.yaml")
	Expect(os.WriteFile(p, []byte(override), 0o600)).To(Succeed())
	out, err := exec.Command("helm", "template", "shepherd", "shepherd",
		"-f", "shepherd/ci/default-values.yaml", "-f", p).CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}
	var objs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var doc map[string]any
		decErr := dec.Decode(&doc)
		if errors.Is(decErr, io.EOF) {
			return objs, string(out), nil
		}
		Expect(decErr).NotTo(HaveOccurred(), "helm template output did not parse as YAML")
		if doc != nil {
			objs = append(objs, doc)
		}
	}
}

func isReceiver(o map[string]any) bool {
	labels, _ := dig(o, "metadata", "labels").(map[string]any) //nolint:errcheck // absent labels read as not-receiver
	return labels["app.kubernetes.io/component"] == "receiver"
}

func receiverObject(objs []map[string]any, kind string) map[string]any {
	for _, o := range objs {
		if o["kind"] == kind && isReceiver(o) {
			return o
		}
	}
	Fail("no receiver " + kind + " rendered")
	return nil
}

// dig walks nested maps by key.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

var _ = Describe("receiver tier chart objects", func() {
	It("renders nothing for the receiver by default (the off-switch)", func() {
		objs, out, err := helmTemplate("{}")
		Expect(err).NotTo(HaveOccurred(), out)
		for _, o := range objs {
			Expect(isReceiver(o)).To(BeFalse(), "default render produced a receiver %v", o["kind"])
		}
	})

	It("renders the Deployment, Service, NetworkPolicy, ConfigMap and ServiceAccount when enabled", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		for _, kind := range []string{"Deployment", "Service", "NetworkPolicy", "ConfigMap", "ServiceAccount"} {
			receiverObject(objs, kind)
		}
	})

	DescribeTable("refuses to render without the NetworkPolicy's required lists",
		func(drop, wantMsg string) {
			_, out, err := helmTemplate(strings.Replace(receiverOn, drop, "", 1))
			Expect(err).To(HaveOccurred())
			Expect(out).To(ContainSubstring(wantMsg))
		},
		Entry("no gatewayFrom", "    gatewayFrom:\n      - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: gateway}}\n"+
			"        podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: edge}}\n",
			"receiver.networkPolicy.gatewayFrom must name the gateway"),
		Entry("a gatewayFrom peer with only a namespace (any pod in it could assert any tenant)",
			"        podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: edge}}\n",
			"gatewayFrom[0] must include a non-empty podSelector"),
		Entry("no egress", "    egress:\n      - to: [{ipBlock: {cidr: 10.0.0.0/8}}]\n",
			"receiver.networkPolicy.egress must allow"),
	)

	It("admits only the gateway, on the OTLP port, and denies other egress apart from DNS", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		np := receiverObject(objs, "NetworkPolicy")
		Expect(dig(np, "spec", "policyTypes")).To(ConsistOf("Ingress", "Egress"))

		ingress, _ := dig(np, "spec", "ingress").([]any) //nolint:errcheck // asserted below
		Expect(ingress).To(HaveLen(1))
		Expect(dig(ingress[0], "from")).To(Equal([]any{map[string]any{
			"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "gateway"}},
			"podSelector":       map[string]any{"matchLabels": map[string]any{"gateway.networking.k8s.io/gateway-name": "edge"}},
		}}))
		Expect(dig(ingress[0], "ports")).To(Equal([]any{map[string]any{"port": "otlp-http", "protocol": "TCP"}}))

		egress, _ := dig(np, "spec", "egress").([]any) //nolint:errcheck // asserted below
		Expect(egress).To(HaveLen(2), "DNS plus the operator's single rule")
		Expect(dig(egress[1], "to")).To(Equal([]any{map[string]any{"ipBlock": map[string]any{"cidr": "10.0.0.0/8"}}}))
	})

	It("exposes OTLP/HTTP only", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		ports, _ := dig(receiverObject(objs, "Service"), "spec", "ports").([]any) //nolint:errcheck // asserted below
		Expect(ports).To(HaveLen(1))
		Expect(dig(ports[0], "name")).To(Equal("otlp-http"))
	})

	It("runs both containers hardened, with no service-account token", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		pod := dig(receiverObject(objs, "Deployment"), "spec", "template", "spec")
		Expect(dig(pod, "automountServiceAccountToken")).To(BeFalse())
		Expect(dig(pod, "securityContext", "runAsNonRoot")).To(BeTrue())

		inits, _ := dig(pod, "initContainers").([]any) //nolint:errcheck // asserted below
		mains, _ := dig(pod, "containers").([]any)     //nolint:errcheck // asserted below
		Expect(inits).To(HaveLen(1))
		Expect(mains).To(HaveLen(1))
		for _, c := range append(inits, mains...) {
			Expect(dig(c, "securityContext", "readOnlyRootFilesystem")).To(BeTrue(), "container %v", dig(c, "name"))
			Expect(dig(c, "securityContext", "allowPrivilegeEscalation")).To(BeFalse(), "container %v", dig(c, "name"))
			Expect(dig(c, "securityContext", "capabilities", "drop")).To(Equal([]any{"ALL"}), "container %v", dig(c, "name"))
		}
		Expect(dig(inits[0], "command")).To(ContainElements("receiver", "render"))
	})

	It("keeps secret values out of the ConfigMap, passing them to Alloy as env from Secrets", func() {
		objs, out, err := helmTemplate(receiverOn)
		Expect(err).NotTo(HaveOccurred(), out)
		cfg, _ := dig(receiverObject(objs, "ConfigMap"), "data", "receiver.yaml").(string) //nolint:errcheck // asserted below
		Expect(cfg).To(ContainSubstring(`endpoint_env: "RECEIVER_METRICS_ENDPOINT"`))
		Expect(cfg).To(ContainSubstring(`Authorization: "RECEIVER_METRICS_AUTHORIZATION"`))

		mains, _ := dig(receiverObject(objs, "Deployment"), "spec", "template", "spec", "containers").([]any) //nolint:errcheck // asserted below
		env, _ := dig(mains[0], "env").([]any)                                                                //nolint:errcheck // asserted below
		refs := map[string]any{}
		for _, e := range env {
			name, _ := dig(e, "name").(string) //nolint:errcheck // shape known
			refs[name] = dig(e, "valueFrom", "secretKeyRef", "name")
		}
		Expect(refs).To(HaveKeyWithValue("RECEIVER_METRICS_ENDPOINT", "mimir-endpoint"))
		Expect(refs).To(HaveKeyWithValue("RECEIVER_METRICS_AUTHORIZATION", "mimir-auth"))
	})

	It("refuses an empty podSelector, which would match every pod in the namespace", func() {
		_, out, err := helmTemplate(strings.Replace(receiverOn,
			"podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: edge}}", "podSelector: {}", 1))
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("gatewayFrom[0] must include a non-empty podSelector"))
	})

	It("rejects an unsupported mode through the values schema", func() {
		_, out, err := helmTemplate(receiverOn + "  mode: grpc\n")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("at '/receiver/mode': value must be one of 'pass_through', 'static'"))
	})
})
