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

// renderWith runs the real `helm template` over the chart with an inline
// values override and returns every object keyed by "Kind/Name", the same way
// renderSimulatorEnabled does. Reading assertions back out of actual helm
// output is the point: the ServiceMonitor this file covers was wrong for its
// entire life while `helm lint` and every render test stayed green, because
// nothing ever checked that the port it scraped was a port that serves
// metrics.
func renderWith(values string) map[string]map[string]any {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	overridePath := filepath.Join(dir, "override.yaml")
	Expect(os.WriteFile(overridePath, []byte(values), 0o600)).To(Succeed())

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
		// A document missing kind/metadata.name is not a Kubernetes object
		// worth indexing; the zero values are filtered immediately below.
		kind, _ := doc["kind"].(string)             //nolint:errcheck // see above
		meta, _ := doc["metadata"].(map[string]any) //nolint:errcheck // see above
		name, _ := meta["name"].(string)            //nolint:errcheck // see above
		if kind == "" || name == "" {
			continue
		}
		objects[kind+"/"+name] = doc
	}
	return objects
}

var _ = Describe("chart observability wiring", func() {
	Describe("metrics", func() {
		It("scrapes the metrics port, not the API port", func() {
			// The defect this replaces: the ServiceMonitor scraped `port: http`
			// (:8080) at /metrics, which internal/server 404-guards because
			// metrics were moved to their own listener. Enabling it produced a
			// permanently failing Prometheus target and no metrics at all.
			objs := renderWith("metrics:\n  serviceMonitor:\n    enabled: true\n")

			sm, ok := objs["ServiceMonitor/shepherd"]
			Expect(ok).To(BeTrue(), "ServiceMonitor not rendered")
			spec := asMap(sm["spec"])
			endpoints := asSlice(spec["endpoints"])
			Expect(endpoints).To(HaveLen(1))
			ep := asMap(endpoints[0])
			Expect(ep["port"]).To(Equal("metrics"), "must scrape the metrics port; \"http\" is 404-guarded for /metrics")
			Expect(ep["path"]).To(Equal("/metrics"))

			// And it must select the metrics Service specifically — both
			// Services carry identical selector labels otherwise.
			selector := asMap(asMap(spec["selector"])["matchLabels"])
			Expect(selector).To(HaveKeyWithValue("app.kubernetes.io/component", "metrics"))
		})

		It("exposes the metrics port on a ClusterIP Service, never the public one", func() {
			// service.type is operator-configurable; putting metrics on the
			// main Service would publish the listener the moment anyone chose
			// LoadBalancer or NodePort.
			objs := renderWith("service:\n  type: LoadBalancer\n")

			metricsSvc, ok := objs["Service/shepherd-metrics"]
			Expect(ok).To(BeTrue(), "metrics Service not rendered")
			Expect(asMap(metricsSvc["spec"])["type"]).To(Equal("ClusterIP"),
				"the metrics Service must stay ClusterIP even when the API Service is a LoadBalancer")

			mainSvc := asMap(objs["Service/shepherd"]["spec"])
			for _, p := range asSlice(mainSvc["ports"]) {
				Expect(asMap(p)["name"]).NotTo(Equal("metrics"),
					"the metrics port must not be on the externally-exposed Service")
			}
		})

		It("derives the container port, Service port, and config from one value", func() {
			// Three places used to be able to disagree, and did.
			objs := renderWith("config:\n  server:\n    metrics_listen: \":9231\"\n")

			svcPorts := asSlice(asMap(objs["Service/shepherd-metrics"]["spec"])["ports"])
			Expect(asMap(svcPorts[0])["port"]).To(Equal(9231))

			containers := asSlice(asMap(asMap(asMap(objs["Deployment/shepherd"]["spec"])["template"])["spec"])["containers"])
			var found bool
			for _, c := range containers {
				for _, p := range asSlice(asMap(c)["ports"]) {
					port := asMap(p)
					if port["name"] == "metrics" {
						found = true
						Expect(port["containerPort"]).To(Equal(9231))
					}
				}
			}
			Expect(found).To(BeTrue(), "no container port named \"metrics\"")
		})

		It("omits the metrics Service and ServiceMonitor when metrics are disabled", func() {
			objs := renderWith("metrics:\n  enabled: false\n  serviceMonitor:\n    enabled: true\n")
			Expect(objs).NotTo(HaveKey("Service/shepherd-metrics"))
			Expect(objs).NotTo(HaveKey("ServiceMonitor/shepherd"),
				"a ServiceMonitor pointing at a Service that was not rendered is a broken target")
		})
	})

	Describe("tracing", func() {
		It("is off in the rendered config by default", func() {
			objs := renderWith("{}\n")
			cfg := shepherdConfig(objs)
			tracing, ok := cfg["tracing"].(map[string]any)
			Expect(ok).To(BeTrue(), "tracing block missing from shepherd.yaml")
			Expect(tracing["enabled"]).To(BeFalse())
		})

		It("carries the operator's endpoint through to the config when enabled", func() {
			objs := renderWith("config:\n  tracing:\n    enabled: true\n    endpoint: \"otel-collector.observability.svc:4317\"\n    sample_ratio: 1.0\n")
			tracing := asMap(shepherdConfig(objs)["tracing"])
			Expect(tracing["enabled"]).To(BeTrue())
			Expect(tracing["endpoint"]).To(Equal("otel-collector.observability.svc:4317"))
			// YAML renders 1.0 as 1, so this arrives as an int through helm.
			// Harmless: viper/mapstructure converts it to float64 on load
			// (verified against config.Load), so the assertion is on the
			// numeric value rather than its YAML type.
			Expect(tracing["sample_ratio"]).To(BeNumerically("==", 1.0))
			Expect(tracing["protocol"]).To(Equal("grpc"), "protocol default must survive a partial override")
		})
	})
})

// shepherdConfig parses the shepherd.yaml the ConfigMap carries.
func shepherdConfig(objs map[string]map[string]any) map[string]any {
	GinkgoHelper()
	cm, ok := objs["ConfigMap/shepherd"]
	Expect(ok).To(BeTrue(), "ConfigMap not rendered")
	raw, ok := asMap(cm["data"])["shepherd.yaml"].(string)
	Expect(ok).To(BeTrue(), "shepherd.yaml missing from the ConfigMap")
	var cfg map[string]any
	Expect(yaml.Unmarshal([]byte(raw), &cfg)).To(Succeed())
	return cfg
}

// asMap and asSlice are checked type assertions that fail the spec with a
// readable message instead of panicking. Rendered YAML is `any` all the way
// down, so a chart change that alters a document's shape should say which
// field surprised it, not produce a bare interface-conversion panic.
func asMap(v any) map[string]any {
	GinkgoHelper()
	m, ok := v.(map[string]any)
	Expect(ok).To(BeTrue(), "expected a YAML mapping, got %T", v)
	return m
}

func asSlice(v any) []any {
	GinkgoHelper()
	sl, ok := v.([]any)
	Expect(ok).To(BeTrue(), "expected a YAML sequence, got %T", v)
	return sl
}
