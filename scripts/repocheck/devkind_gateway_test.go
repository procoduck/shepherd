package repocheck_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// gatewayDocs decodes a repo-relative multi-document YAML file (documents
// separated by "---") into a slice of generic maps, one per document. These
// specs assert manifest shape as plain maps rather than typed Gateway API /
// core/v1 structs -- see docs/archive/plans/2026-09-14-kind-dev-stack.md §1: "parse
// manifests with yaml.v3 ... instead of kubectl apply --dry-run", the same
// approach scripts/repocheck/helpers_test.go's loadYAML takes for one-document
// files.
func gatewayDocs(rel string) []map[string]any {
	GinkgoHelper()
	dec := yaml.NewDecoder(strings.NewReader(readRepoFile(rel)))
	var docs []map[string]any
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		Expect(err).NotTo(HaveOccurred(), rel)
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// gwMap type-asserts v as a YAML mapping, failing the spec with a useful
// message instead of panicking on a bad shape.
func gwMap(v any, what string) map[string]any {
	GinkgoHelper()
	m, ok := v.(map[string]any)
	Expect(ok).To(BeTrue(), "%s: expected a mapping, got %T (%v)", what, v, v)
	return m
}

// gwSlice type-asserts v as a YAML sequence.
func gwSlice(v any, what string) []any {
	GinkgoHelper()
	s, ok := v.([]any)
	Expect(ok).To(BeTrue(), "%s: expected a sequence, got %T (%v)", what, v, v)
	return s
}

// Red run, 2026-09-14 (docs/archive/plans/2026-09-14-kind-dev-stack.md, slice K5 "gateway-oidc"):
// dev/kind/gateway.yaml, dev/kind/oidc.yaml and dev/kind/routes.yaml did not exist. Every
// assertion below failed with:
//
//	open <repo>/dev/kind/gateway.yaml: no such file or directory
//
// (readRepoFile wraps os.ReadFile and Expect()s the error away, so the Ginkgo failure text is
// exactly that os error against the missing path.)
var _ = Describe("dev/kind gateway, mock OIDC and the hand-written HTTPRoutes", func() {
	Context("dev/kind/gateway.yaml", func() {
		It("defines the shared dev Gateway with one HTTP:80 listener on the nginx GatewayClass", func() {
			docs := gatewayDocs("dev/kind/gateway.yaml")
			Expect(docs).To(HaveLen(1), "gateway.yaml should hold exactly one document")

			gw := docs[0]
			Expect(gw["kind"]).To(Equal("Gateway"))
			Expect(gw["apiVersion"]).To(Equal("gateway.networking.k8s.io/v1"))

			meta := gwMap(gw["metadata"], "gateway.metadata")
			Expect(meta["name"]).To(Equal("dev"))
			Expect(meta["namespace"]).To(Equal("shepherd-dev"))

			spec := gwMap(gw["spec"], "gateway.spec")
			Expect(spec["gatewayClassName"]).To(Equal("nginx"))

			listeners := gwSlice(spec["listeners"], "gateway.spec.listeners")
			Expect(listeners).To(HaveLen(1), "exactly one listener")
			l := gwMap(listeners[0], "gateway.spec.listeners[0]")
			Expect(l["port"]).To(Equal(80))
			Expect(l["protocol"]).To(Equal("HTTP"))
		})
	})

	Context("dev/kind/routes.yaml", func() {
		var routesByName map[string]map[string]any

		BeforeEach(func() {
			docs := gatewayDocs("dev/kind/routes.yaml")
			Expect(docs).To(HaveLen(2), "exactly two HTTPRoutes: oidc and gitea")
			routesByName = map[string]map[string]any{}
			for _, d := range docs {
				Expect(d["kind"]).To(Equal("HTTPRoute"))
				meta := gwMap(d["metadata"], "route.metadata")
				name, ok := meta["name"].(string)
				Expect(ok).To(BeTrue())
				routesByName[name] = d
			}
		})

		routeBackend := func(doc map[string]any) (hostnames []string, parentRefNames []string, backendName string, backendPort int) {
			spec := gwMap(doc["spec"], "route.spec")
			for _, h := range gwSlice(spec["hostnames"], "route.spec.hostnames") {
				hostnames = append(hostnames, fmt.Sprint(h))
			}
			for _, p := range gwSlice(spec["parentRefs"], "route.spec.parentRefs") {
				pm := gwMap(p, "route.spec.parentRefs[]")
				parentRefNames = append(parentRefNames, fmt.Sprint(pm["name"]))
			}
			rules := gwSlice(spec["rules"], "route.spec.rules")
			Expect(rules).NotTo(BeEmpty())
			rule := gwMap(rules[0], "route.spec.rules[0]")
			backendRefs := gwSlice(rule["backendRefs"], "route.spec.rules[0].backendRefs")
			Expect(backendRefs).To(HaveLen(1))
			br := gwMap(backendRefs[0], "route.spec.rules[0].backendRefs[0]")
			backendName = fmt.Sprint(br["name"])
			portVal, ok := br["port"].(int)
			Expect(ok).To(BeTrue(), "backendRefs[0].port should decode as an int, got %T", br["port"])
			backendPort = portVal
			return
		}

		It("routes oidc.localtest.me to the oidc Service on 8090", func() {
			doc, ok := routesByName["oidc"]
			Expect(ok).To(BeTrue(), "no HTTPRoute named oidc")
			hostnames, parents, backendName, backendPort := routeBackend(doc)
			Expect(hostnames).To(ConsistOf("oidc.localtest.me"))
			Expect(parents).To(ConsistOf("dev"))
			Expect(backendName).To(Equal("oidc"))
			Expect(backendPort).To(Equal(8090))
		})

		It("routes gitea.localtest.me to the gitea Service on 3000", func() {
			doc, ok := routesByName["gitea"]
			Expect(ok).To(BeTrue(), "no HTTPRoute named gitea")
			hostnames, parents, backendName, backendPort := routeBackend(doc)
			Expect(hostnames).To(ConsistOf("gitea.localtest.me"))
			Expect(parents).To(ConsistOf("dev"))
			Expect(backendName).To(Equal("gitea"))
			Expect(backendPort).To(Equal(3000))
		})

		It("does not carry the shepherd route -- that one is the chart's (K3)", func() {
			Expect(readRepoFile("dev/kind/routes.yaml")).NotTo(ContainSubstring("shepherd.localtest.me"))
			_, ok := routesByName["shepherd"]
			Expect(ok).To(BeFalse())
		})
	})

	Context("dev/kind/oidc.yaml", func() {
		// composeOIDCImage reads dev/docker-compose.dev.yaml's services.oidc.image so this spec
		// fails the moment the two pins drift apart, instead of hand-duplicating the string.
		composeOIDCImage := func() string {
			GinkgoHelper()
			var compose map[string]any
			loadYAML("dev/docker-compose.dev.yaml", &compose)
			services := gwMap(compose["services"], "compose.services")
			oidcSvc := gwMap(services["oidc"], "compose.services.oidc")
			img, ok := oidcSvc["image"].(string)
			Expect(ok).To(BeTrue())
			return img
		}

		var deployment, service map[string]any

		BeforeEach(func() {
			docs := gatewayDocs("dev/kind/oidc.yaml")
			for _, d := range docs {
				switch d["kind"] {
				case "Deployment":
					deployment = d
				case "Service":
					service = d
				}
			}
			Expect(deployment).NotTo(BeNil(), "no Deployment in oidc.yaml")
			Expect(service).NotTo(BeNil(), "no Service in oidc.yaml")
		})

		It("matches the compose oidc image, sets SERVER_PORT 8090 and carries a JSON_CONFIG with interactiveLogin true", func() {
			spec := gwMap(deployment["spec"], "deployment.spec")
			tmpl := gwMap(spec["template"], "deployment.spec.template")
			podSpec := gwMap(tmpl["spec"], "deployment.spec.template.spec")
			containers := gwSlice(podSpec["containers"], "deployment.spec.template.spec.containers")
			Expect(containers).To(HaveLen(1))
			c := gwMap(containers[0], "containers[0]")

			Expect(c["image"]).To(Equal(composeOIDCImage()), "oidc image must match dev/docker-compose.dev.yaml's services.oidc.image")

			env := gwSlice(c["env"], "containers[0].env")
			envValue := map[string]string{}
			for _, e := range env {
				em := gwMap(e, "containers[0].env[]")
				name, ok := em["name"].(string)
				Expect(ok).To(BeTrue(), "containers[0].env[].name should decode as a string")
				envValue[name] = fmt.Sprint(em["value"])
			}
			Expect(envValue["SERVER_PORT"]).To(Equal("8090"))

			jsonConfig, ok := envValue["JSON_CONFIG"]
			Expect(ok).To(BeTrue(), "no JSON_CONFIG env var")
			var parsed map[string]any
			Expect(json.Unmarshal([]byte(jsonConfig), &parsed)).To(Succeed(), "JSON_CONFIG must be valid JSON:\n%s", jsonConfig)
			Expect(parsed["interactiveLogin"]).To(Equal(true))
		})

		It("exposes a Service named oidc on port 8090", func() {
			meta := gwMap(service["metadata"], "service.metadata")
			Expect(meta["name"]).To(Equal("oidc"))
			svcSpec := gwMap(service["spec"], "service.spec")
			ports := gwSlice(svcSpec["ports"], "service.spec.ports")
			found := false
			for _, p := range ports {
				pm := gwMap(p, "service.spec.ports[]")
				if pm["port"] == 8090 {
					found = true
				}
			}
			Expect(found).To(BeTrue(), "no port 8090 on the oidc Service")
		})
	})
})
