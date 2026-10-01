package helm_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// #234: every pod this chart runs -- the server, the simulator, the receiver,
// the migrate Job -- carries the release-wide app.kubernetes.io/name+instance
// pair, so a selector of that pair alone matched all of them. Both app
// Services' EndpointSlices listed the simulator pod, `kubectl port-forward
// svc/shepherd` could land on it, the app PDB counted it, and the app
// NetworkPolicy's allow-all egress applied to the sandbox. These specs read
// every selector back out of real `helm template` output and evaluate it
// against every rendered pod template, both directions.

// allTiersOn renders every pod-bearing and selector-bearing object at once:
// simulator, receiver, the app NetworkPolicy, the PDB (needs 2+ replicas) and
// the metrics Service.
const allTiersOn = receiverOn + `
simulator:
  enabled: true
networkPolicy:
  enabled: true
replicas: 2
metrics:
  enabled: true
`

// selectorMatches is Kubernetes' matchLabels semantics: every selector key
// present on the pod with the same value. An empty selector would match
// everything; none of the chart's may be empty, so that is a failure here.
func selectorMatches(selector, podLabels map[string]any) bool {
	GinkgoHelper()
	Expect(selector).NotTo(BeEmpty(), "an empty selector matches every pod")
	for k, v := range selector {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

func objectNamed(objs []map[string]any, kind, name string) map[string]any {
	GinkgoHelper()
	for _, o := range objs {
		if o["kind"] == kind && dig(o, "metadata", "name") == name {
			return o
		}
	}
	Fail("not rendered: " + kind + "/" + name)
	return nil
}

func asLabels(v any) map[string]any {
	GinkgoHelper()
	m, ok := v.(map[string]any)
	Expect(ok).To(BeTrue(), "expected a label map, got %#v", v)
	return m
}

var _ = Describe("Helm chart: app selectors match only the server pods (#234)", func() {
	var (
		objs   []map[string]any
		server map[string]any
		others map[string]map[string]any // pod-template labels of every NON-server pod
	)

	BeforeEach(func() {
		var out string
		var err error
		objs, out, err = helmTemplate(allTiersOn)
		Expect(err).NotTo(HaveOccurred(), out)

		server = asLabels(dig(objectNamed(objs, "Deployment", "shepherd"), "spec", "template", "metadata", "labels"))
		others = map[string]map[string]any{
			"simulator": asLabels(dig(objectNamed(objs, "Deployment", "shepherd-simulator"), "spec", "template", "metadata", "labels")),
			"receiver":  asLabels(dig(objectNamed(objs, "Deployment", "shepherd-receiver"), "spec", "template", "metadata", "labels")),
			"migrate":   asLabels(dig(objectNamed(objs, "Job", "shepherd-migrate"), "spec", "template", "metadata", "labels")),
		}
	})

	It("labels the server pods component=server", func() {
		Expect(server).To(HaveKeyWithValue("app.kubernetes.io/component", "server"))
	})

	// The objects that must reach the server pods and nothing else.
	DescribeTable("selects the server pods and no other pod of the release",
		func(kind, name string, path ...string) {
			sel := asLabels(dig(objectNamed(objs, kind, name), path...))
			Expect(selectorMatches(sel, server)).To(BeTrue(), "%s/%s does not select the server pods", kind, name)
			for tier, labels := range others {
				Expect(selectorMatches(sel, labels)).To(BeFalse(),
					"%s/%s also selects the %s pods (%v)", kind, name, tier, sel)
			}
		},
		Entry("Service/shepherd", "Service", "shepherd", "spec", "selector"),
		Entry("Service/shepherd-metrics", "Service", "shepherd-metrics", "spec", "selector"),
		Entry("PodDisruptionBudget/shepherd", "PodDisruptionBudget", "shepherd", "spec", "selector", "matchLabels"),
		Entry("NetworkPolicy/shepherd podSelector", "NetworkPolicy", "shepherd", "spec", "podSelector", "matchLabels"),
	)

	It("lets only the server pods reach the simulator's control port", func() {
		np := objectNamed(objs, "NetworkPolicy", "shepherd-simulator")
		ingress, ok := dig(np, "spec", "ingress").([]any)
		Expect(ok).To(BeTrue())
		Expect(ingress).To(HaveLen(1))
		from, ok := dig(ingress[0], "from").([]any)
		Expect(ok).To(BeTrue())
		Expect(from).To(HaveLen(1))
		sel := asLabels(dig(from[0], "podSelector", "matchLabels"))
		Expect(selectorMatches(sel, server)).To(BeTrue())
		for tier, labels := range others {
			Expect(selectorMatches(sel, labels)).To(BeFalse(), "the %s pods may reach the simulator's control port", tier)
		}
	})

	// The other direction: the simulator's and receiver's own selectors stay
	// off the server pods.
	DescribeTable("does not select the server pods",
		func(kind, name string, path ...string) {
			sel := asLabels(dig(objectNamed(objs, kind, name), path...))
			Expect(selectorMatches(sel, server)).To(BeFalse(), "%s/%s selects the server pods (%v)", kind, name, sel)
		},
		Entry("Service/shepherd-simulator", "Service", "shepherd-simulator", "spec", "selector"),
		Entry("Deployment/shepherd-simulator", "Deployment", "shepherd-simulator", "spec", "selector", "matchLabels"),
		Entry("NetworkPolicy/shepherd-simulator podSelector", "NetworkPolicy", "shepherd-simulator", "spec", "podSelector", "matchLabels"),
		Entry("Service/shepherd-receiver", "Service", "shepherd-receiver", "spec", "selector"),
		Entry("Deployment/shepherd-receiver", "Deployment", "shepherd-receiver", "spec", "selector", "matchLabels"),
		Entry("NetworkPolicy/shepherd-receiver podSelector", "NetworkPolicy", "shepherd-receiver", "spec", "podSelector", "matchLabels"),
	)

	It("keeps the app Deployment's immutable selector exactly as earlier charts rendered it", func() {
		// spec.selector cannot change on a live Deployment: adding the
		// component label here would make every `helm upgrade` from an
		// earlier chart fail. It still matches the server pods, which is all
		// it has to do.
		sel := asLabels(dig(objectNamed(objs, "Deployment", "shepherd"), "spec", "selector", "matchLabels"))
		Expect(sel).To(Equal(map[string]any{
			"app.kubernetes.io/name":     "shepherd",
			"app.kubernetes.io/instance": "shepherd",
		}))
		Expect(selectorMatches(sel, server)).To(BeTrue())
	})
})
