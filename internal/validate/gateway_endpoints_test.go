package validate_test

import (
	"context"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/validate"
)

// The org owns the OTLP route acme-1a2b3c4d and the Faro route acme-f00f00;
// globex-9z8y7x6w is another org's OTLP route.
const (
	ownOTLP   = "acme-1a2b3c4d"
	ownFaro   = "acme-f00f00"
	otherOTLP = "globex-9z8y7x6w"
)

// gatewayPolicy is strictFor() with a gateway answering to hosts.
func gatewayPolicy(hosts ...string) validate.TenantPolicy {
	p := strictFor()
	if len(hosts) == 0 {
		hosts = []string{"gw.example.com"}
	}
	var norm []string
	for _, h := range hosts {
		n, err := validate.NormalizeGatewayHost(h)
		Expect(err).NotTo(HaveOccurred(), h)
		norm = append(norm, n)
	}
	p.Gateway = validate.GatewayRoutes{Hosts: norm, Owns: func(kind, segment string) bool {
		return (kind == "otlp" && segment == ownOTLP) || (kind == "faro" && segment == ownFaro)
	}}
	return p
}

func otlphttp(endpoint string) string {
	return "otelcol.exporter.otlphttp \"o\" {\n  client {\n    endpoint = " + endpoint + "\n  }\n}\n"
}

func gwLokiWrite(url string) string {
	return "loki.write \"w\" {\n  endpoint {\n    url = " + url + "\n  }\n}\n"
}

func gatewayRefusals(src string, p validate.TenantPolicy) []string {
	var out []string
	for _, m := range errorsOf(validate.TenantLint(src, p)) {
		if strings.Contains(m, validate.GatewayEndpointRefusal) {
			out = append(out, m)
		}
	}
	return out
}

var _ = Describe("strict tenant mode checks gateway route endpoints", func() {
	q := func(s string) string { return `"` + s + `"` }

	It("accepts an endpoint under one of the org's own routes, as the gateway's path prefix matches it", func() {
		for _, src := range []string{
			otlphttp(q("https://gw.example.com/otlp/" + ownOTLP)),
			otlphttp(q("https://gw.example.com/otlp/" + ownOTLP + "/")),
			otlphttp(q("https://gw.example.com/otlp/" + ownOTLP + "?x=1")),
			gwLokiWrite(q("https://gw.example.com/otlp/" + ownOTLP + "/loki/api/v1/push")),
			"otelcol.exporter.otlphttp \"o\" {\n  traces_endpoint = " + q("https://gw.example.com/otlp/"+ownOTLP+"/v1/traces") +
				"\n  client {\n    endpoint = \"https://otlp.example.net\"\n  }\n}\n",
			"otelcol.exporter.faro \"f\" {\n  client {\n    endpoint = " + q("https://gw.example.com/faro/"+ownFaro+"/collect") + "\n  }\n}\n",
		} {
			Expect(validate.TenantLint(src, gatewayPolicy())).To(BeEmpty(), src)
		}
	})

	It("accepts endpoints on any other host, whatever their path", func() {
		for _, src := range []string{
			otlphttp(q("https://otlp.example.net/otlp/" + otherOTLP)),
			gwLokiWrite(q("http://loki:3100/loki/api/v1/push")),
			otlphttp(q("https://gw.example.com.evil.example/otlp/" + otherOTLP)),
			otlphttp(q("https://xgw.example.com/otlp/" + otherOTLP)),
			"otelcol.exporter.otlp \"g\" {\n  client {\n    endpoint = \"tempo:4317\"\n  }\n}\n",
		} {
			Expect(validate.TenantLint(src, gatewayPolicy())).To(BeEmpty(), src)
		}
	})

	It("refuses an endpoint under another org's route, a route of the other kind, or no route at all", func() {
		for _, ep := range []string{
			"https://gw.example.com/otlp/" + otherOTLP,
			"https://gw.example.com/otlp/" + otherOTLP + "/v1/logs",
			"https://gw.example.com/faro/" + ownOTLP,
			"https://gw.example.com/otlp/" + ownFaro,
			"https://gw.example.com/otlp/" + ownOTLP + "x",
			"https://gw.example.com/OTLP/" + ownOTLP,
			"https://gw.example.com/otlp/",
			"https://gw.example.com/otlp",
			"https://gw.example.com/v1/traces",
			"https://gw.example.com",
		} {
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy())).To(HaveLen(1), ep)
		}
		Expect(gatewayRefusals(gwLokiWrite(q("https://gw.example.com/otlp/"+otherOTLP+"/loki/api/v1/push")), gatewayPolicy())).
			To(ConsistOf(SatisfyAll(ContainSubstring("loki.write endpoint url"), ContainSubstring(otherOTLP))))
		// A gRPC endpoint names no path, so it reaches no route of the org.
		Expect(gatewayRefusals("otelcol.exporter.otlp \"g\" {\n  client {\n    endpoint = \"gw.example.com:443\"\n  }\n}\n",
			gatewayPolicy())).To(HaveLen(1))
		Expect(gatewayRefusals("otelcol.exporter.otlp \"g\" {\n  client {\n    endpoint = \"dns:///gw.example.com:443\"\n  }\n}\n",
			gatewayPolicy())).To(HaveLen(1))
	})

	It("compares hosts as a client resolves them: case, trailing dots, ports, userinfo, IDN and percent-encoding", func() {
		for _, ep := range []string{
			"https://GW.Example.COM/otlp/" + otherOTLP,
			"https://gw.example.com./otlp/" + otherOTLP,
			"https://gw.example.com:443/otlp/" + otherOTLP,
			"http://gw.example.com:8080/otlp/" + otherOTLP,
			"https://someone@gw.example.com/otlp/" + otherOTLP,
			"https://gw%2Eexample.com/otlp/" + otherOTLP,
			"https://gw。example。com/otlp/" + otherOTLP,
		} {
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy())).To(HaveLen(1), ep)
		}
		for _, ep := range []string{"https://bücher.example/otlp/" + otherOTLP, "https://xn--bcher-kva.example/otlp/" + otherOTLP} {
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy("bücher.example"))).To(HaveLen(1), ep)
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy("xn--bcher-kva.example"))).To(HaveLen(1), ep)
		}
		for _, ep := range []string{
			"http://10.0.0.5/otlp/" + otherOTLP,
			"http://[::ffff:10.0.0.5]/otlp/" + otherOTLP,
			"http://10.0.0.05/otlp/" + otherOTLP,
			"http://167772165/otlp/" + otherOTLP,
			"http://0xa.0.0.5/otlp/" + otherOTLP,
		} {
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy("10.0.0.5"))).To(HaveLen(1), ep)
		}
		Expect(validate.TenantLint(otlphttp(q("https://GW.example.com:8443/otlp/"+ownOTLP)), gatewayPolicy())).To(BeEmpty())
	})

	It("refuses a path on the gateway that is not written plainly", func() {
		for _, ep := range []string{
			"https://gw.example.com/otlp/" + ownOTLP + "/../" + otherOTLP + "/v1/logs",
			"https://gw.example.com/otlp/" + ownOTLP + "/%2e%2e/" + otherOTLP,
			"https://gw.example.com//otlp/" + ownOTLP,
			"https://gw.example.com/./otlp/" + ownOTLP,
			"https://gw.example.com/otlp/%61cme-1a2b3c4d",
			"https://gw.example.com/otlp/" + ownOTLP + `\\..\\` + otherOTLP,
		} {
			Expect(gatewayRefusals(otlphttp(q(ep)), gatewayPolicy())).To(HaveLen(1), ep)
		}
	})

	It("refuses an endpoint it cannot read unless what it can read proves the host is not the gateway", func() {
		for _, v := range []string{
			`sys.env("OTLP_ENDPOINT")`,
			`"https://" + sys.env("HOST") + "/otlp/` + ownOTLP + `"`,
			`"https://gw.example.com/otlp/" + sys.env("SEGMENT")`,
			`"https://gw.example.com" + sys.env("PATH")`,
			`local.file.endpoint.content`,
			`remote.http.ep.content`,
		} {
			Expect(gatewayRefusals(otlphttp(v), gatewayPolicy())).To(HaveLen(1), v)
		}
		Expect(validate.TenantLint(otlphttp(`"https://otlp.example.net/" + sys.env("PATH")`), gatewayPolicy())).To(BeEmpty())
		Expect(validate.TenantLint(otlphttp(`"https://gw.example.com/otlp/`+ownOTLP+`/" + sys.env("PATH")`), gatewayPolicy())).
			To(BeEmpty(), "the route segment is fixed once the path has gone past it")
	})

	It("follows declare and module arguments to literals, as for tenants", func() {
		declare := "declare \"out\" {\n  argument \"ep\" { }\n" + indentBy(otlphttp("argument.ep.value"), "  ") + "}\n"
		own := declare + "out \"a\" {\n  ep = \"https://gw.example.com/otlp/" + ownOTLP + "\"\n}\n"
		Expect(validate.TenantLint(own, gatewayPolicy())).To(BeEmpty())
		other := declare + "out \"a\" {\n  ep = \"https://gw.example.com/otlp/" + ownOTLP + "\"\n}\n" +
			"out \"b\" {\n  ep = \"https://gw.example.com/otlp/" + otherOTLP + "\"\n}\n"
		Expect(gatewayRefusals(other, gatewayPolicy())).To(HaveLen(1))
		computed := declare + "out \"a\" {\n  ep = sys.env(\"EP\")\n}\n"
		Expect(gatewayRefusals(computed, gatewayPolicy())).To(HaveLen(1))
		// A declare instantiated in a foreach template is given its value per item: unreadable.
		inForeach := declare + "foreach \"f\" {\n  collection = [\"https://gw.example.com/otlp/" + ownOTLP + "\"]\n  var = \"e\"\n" +
			"  template {\n    out \"a\" {\n      ep = e\n    }\n  }\n}\n"
		Expect(gatewayRefusals(inForeach, gatewayPolicy())).To(HaveLen(1))
		// A literal import.string module, given its value by the importing text.
		module := "import.string \"m\" {\n  content = " + quoteAlloy(declare) + "\n}\n"
		Expect(validate.TenantLint(module+"m.out \"a\" {\n  ep = \"https://gw.example.com/otlp/"+ownOTLP+"\"\n}\n", gatewayPolicy())).To(BeEmpty())
		Expect(gatewayRefusals(module+"m.out \"a\" {\n  ep = \"https://gw.example.com/otlp/"+otherOTLP+"\"\n}\n", gatewayPolicy())).
			To(ConsistOf(ContainSubstring(`import.string "m" line`)))
	})

	It("checks every position the lint walks: a declare, a foreach template, a literal and a pinned module", func() {
		bad := otlphttp(q("https://gw.example.com/otlp/" + otherOTLP))
		inDeclare := "declare \"d\" {\n" + indentBy(bad, "  ") + "}\n"
		template := "foreach \"f\" {\n  collection = [1]\n  var = \"i\"\n  template {\n" + indentBy(bad, "    ") + "  }\n}\n"
		module := "import.string \"m\" {\n  content = " + quoteAlloy("declare \"x\" {\n"+indentBy(bad, "  ")+"}\n") + "\n}\n"
		for _, src := range []string{bad, inDeclare, template, module} {
			Expect(gatewayRefusals(src, gatewayPolicy())).To(HaveLen(1), src)
		}
		m := &fakeModules{
			allowed: map[string]bool{repoA: true},
			modules: map[validate.GitModule]string{{Repository: repoA, Revision: shaA, Path: "m.alloy"}: "declare \"out\" {\n" + bad + "}\n"},
		}
		p := gatewayPolicy()
		p.Git = m
		d, err := validate.TenantLintChecked(context.Background(), gitImport("mod", repoA, shaA, "m.alloy")+"mod.out \"x\" { }\n", p)
		Expect(err).NotTo(HaveOccurred())
		Expect(errorsOf(d)).To(ConsistOf(SatisfyAll(ContainSubstring(`import.git "mod" line`), ContainSubstring(validate.GatewayEndpointRefusal))))
	})

	It("checks list endpoints, proxies, blackbox probe targets and modules, and Datadog's site", func() {
		for _, src := range []string{
			"otelcol.exporter.loadbalancing \"lb\" {\n  resolver {\n    static {\n      hostnames = [\"tempo:4317\", \"gw.example.com:4317\"]\n    }\n  }\n" +
				"  protocol {\n    otlp {\n      client { }\n    }\n  }\n}\n",
			"prometheus.remote_write \"r\" {\n  endpoint {\n    url       = \"https://mimir.example/api/v1/push\"\n" +
				"    proxy_url = \"http://gw.example.com:3128\"\n  }\n}\n",
			"prometheus.exporter.blackbox \"b\" {\n  config = \"modules: {}\"\n  target {\n    name    = \"t\"\n" +
				"    address = \"https://gw.example.com/otlp/" + otherOTLP + "/v1/logs\"\n  }\n}\n",
			"prometheus.exporter.blackbox \"b\" {\n  config  = \"modules: {}\"\n  targets = [{\"name\" = \"t\", \"address\" = \"https://gw.example.com/otlp/" +
				otherOTLP + "\"}]\n}\n",
			"prometheus.exporter.blackbox \"b\" {\n  config  = \"modules: {}\"\n  targets = discovery.http.d.targets\n}\n",
			"prometheus.exporter.blackbox \"b\" {\n  config = \"modules: {p: {prober: http, http: {proxy_url: 'http://GW.example.com'}}}\"\n}\n",
			"otelcol.exporter.datadog \"d\" {\n  api {\n    api_key = \"not-a-real-key\"\n    site    = \"example.com\"\n  }\n}\n",
		} {
			Expect(gatewayRefusals(src, gatewayPolicy())).To(HaveLen(1), src)
		}
		for _, src := range []string{
			"prometheus.remote_write \"r\" {\n  endpoint {\n    url       = \"https://mimir.example/api/v1/push\"\n" +
				"    proxy_url = \"http://proxy.example:3128\"\n  }\n}\n",
			"prometheus.exporter.blackbox \"b\" {\n  config  = \"modules: {}\"\n  targets = [{\"name\" = \"t\", \"address\" = \"https://example.net\"}]\n}\n",
			"otelcol.exporter.datadog \"d\" {\n  api {\n    api_key = \"not-a-real-key\"\n    site    = \"datadoghq.eu\"\n  }\n}\n",
			// Where a component listens, and the TLS server name, are not endpoints.
			"otelcol.receiver.otlp \"r\" {\n  http {\n    endpoint = \"gw.example.com:4318\"\n  }\n  output { }\n}\n",
			"loki.source.api \"a\" {\n  http {\n    listen_address = \"gw.example.com\"\n  }\n  forward_to = []\n}\n",
			"otelcol.exporter.otlphttp \"o\" {\n  client {\n    endpoint = \"https://otlp.example.net\"\n    tls {\n" +
				"      server_name = \"gw.example.com\"\n    }\n  }\n}\n",
		} {
			Expect(validate.TenantLint(src, gatewayPolicy())).To(BeEmpty(), src)
		}
	})

	It("checks nothing when no gateway host is configured, and nothing outside strict mode", func() {
		for _, src := range []string{
			otlphttp(q("https://gw.example.com/otlp/" + otherOTLP)),
			otlphttp(`sys.env("OTLP_ENDPOINT")`),
		} {
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty(), src)
			p := gatewayPolicy()
			p.Strict = false
			Expect(validate.TenantLint(src, p)).To(BeEmpty(), src)
		}
	})

	It("leaves TenantLiterals alone: a gateway endpoint is not a tenant literal", func() {
		_, complete, err := validate.TenantLiterals(otlphttp(`sys.env("OTLP_ENDPOINT")`))
		Expect(err).NotTo(HaveOccurred())
		Expect(complete).To(BeTrue())
	})

	Describe("NormalizeGatewayHost", func() {
		It("normalises a configured host and refuses what is not one", func() {
			for _, c := range [][2]string{
				{"GW.Example.com.", "gw.example.com"},
				{"bücher.example", "xn--bcher-kva.example"},
				{"10.0.0.5", "10.0.0.5"},
				{"[::1]", "::1"},
				{"\tgw.example.com\n", "gw.example.com"},
			} {
				Expect(validate.NormalizeGatewayHost(c[0])).To(Equal(c[1]), c[0])
			}
			for _, bad := range []string{"", "https://gw.example.com", "gw.example.com/otlp", "gw.example.com:443", "a b"} {
				_, err := validate.NormalizeGatewayHost(bad)
				Expect(err).To(HaveOccurred(), bad)
			}
		})
	})
})

func indentBy(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func quoteAlloy(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// TestEndpointAttributeExclusionsExist: every endpoint exclusion names an
// attribute some shipped schema has, so the list cannot go stale.
func TestEndpointAttributeExclusionsExist(t *testing.T) {
	attrs, err := validate.SchemaEndpointAttributes(schema.Embedded)
	if err != nil {
		t.Fatal(err)
	}
	if len(attrs) < 50 {
		t.Fatalf("found only %d endpoint attributes; the schema walk is broken", len(attrs))
	}
	seen := map[string]bool{}
	for _, a := range attrs {
		kind, reason := validate.ClassifyEndpointAttribute(a.Component, a.Block(), a.Attr)
		if kind == validate.EndpointUnclassified {
			t.Errorf("%s %s %s is not classified", a.Component, a.Path, a.Attr)
		}
		if kind == validate.EndpointExcluded {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s %s %s is excluded without a reason", a.Component, a.Path, a.Attr)
			}
			seen[validate.EndpointExclusionKey(a.Component, a.Block(), a.Attr)] = true
		}
	}
	for _, k := range validate.EndpointExclusionKeys() {
		if !seen[k] {
			t.Errorf("endpoint exclusion %q matches no attribute of any shipped schema", k)
		}
	}
}
