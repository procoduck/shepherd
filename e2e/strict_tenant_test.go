//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Strict tenant mode against a real Alloy (#421, `make e2e-strict`). A strict
// org owning the tenant `mine` runs one collector (alloy-strict, cluster
// strict-cluster) whose pipelines read a file and write to tenantsink, a
// recording stand-in for Loki that keeps each push's X-Scope-OrgID and label
// sets. Every construct the tenant lint refuses is refused at save; stored
// directly (as a pipeline saved before the check existed would be), it is
// left out of the served config by the serve-time check. Every construct it
// accepts arrives at the sink — and nothing ever arrives there under any
// tenant but `mine`, which is what would fail if a refused construct were
// served after all. Each pipeline labels its entries case=<its name>.

const (
	sinkURL       = "http://localhost:13100"
	strictOrgName = "e2e-strict"
	strictCluster = "strict-cluster"
	// tailFile is a file the strict collector has: its own config.
	tailFile = "/etc/alloy/config.alloy"
)

// lokiSource reads tailFile into forward, labelled case=name.
func lokiSource(name, forward string) string {
	return fmt.Sprintf(`loki.source.file "src" {
  targets    = [{"__path__" = %q, "case" = %q}]
  forward_to = [%s]
}
`, tailFile, name, forward)
}

// lokiWriter is a loki.write "w" to the sink with endpoint settings extra.
func lokiWriter(extra string) string {
	return `loki.write "w" {
  endpoint {
    url = "http://tenantsink:3100/loki/api/v1/push"
` + extra + `
  }
}
`
}

const toWriter = "loki.write.w.receiver"

// outDeclare is a module writing to the sink under its argument tenant.
const outDeclare = `declare "out" {
  argument "tenant" {
    optional = true
    default  = "mine"
  }

  loki.write "w" {
    endpoint {
      url       = "http://tenantsink:3100/loki/api/v1/push"
      tenant_id = argument.tenant.value
    }
  }

  export "receiver" {
    value = loki.write.w.receiver
  }
}
`

// otlpRoundTrip turns entries into OTLP logs that carry the attribute
// __tenant_id__ = "victim" and name it in the loki.attribute.labels hint, so
// otelcol.exporter.loki would make it a label: tail is what takes
// otelcol.exporter.loki's place.
func otlpRoundTrip(name, exporterInput string) string {
	return lokiSource(name, "otelcol.receiver.loki.r.receiver") + `
otelcol.receiver.loki "r" {
  output {
    logs = [otelcol.processor.attributes.a.input]
  }
}

otelcol.processor.attributes "a" {
  action {
    key    = "__tenant_id__"
    value  = "victim"
    action = "upsert"
  }
  action {
    key    = "loki.attribute.labels"
    value  = "__tenant_id__,case,filename"
    action = "upsert"
  }
  output {
    logs = [` + exporterInput + `]
  }
}
`
}

// accepted are pipelines strict mode accepts: each must arrive at the sink,
// under tenant mine only.
func acceptedCases() map[string]string {
	return map[string]string{
		"a-tenant-id": lokiSource("a-tenant-id", toWriter) + lokiWriter(`    tenant_id = "mine"`),
		"a-header":    lokiSource("a-header", toWriter) + lokiWriter(`    headers = { "X-Scope-OrgID" = "mine" }`),
		"a-declare":   outDeclare + "out \"o\" {\n  tenant = \"mine\"\n}\n" + lokiSource("a-declare", "out.o.receiver"),
		"a-label-map": lokiSource("a-label-map", "loki.process.p.receiver") +
			"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n    values = { \"__tenant_id__\" = \"mine\" }\n  }\n}\n" +
			lokiWriter(""),
		"a-relabel": lokiSource("a-relabel", "loki.relabel.r.receiver") +
			"loki.relabel \"r\" {\n  forward_to = [loki.write.w.receiver]\n  rule {\n    target_label = \"__tenant_id__\"\n    replacement  = \"mine\"\n  }\n}\n" +
			lokiWriter(""),
		// A labelmap that writes __tenant_id__ from the filename label: the
		// strict served form drops it right after (#410).
		"a-labelmap-drop": lokiSource("a-labelmap-drop", "loki.relabel.r.receiver") +
			"loki.relabel \"r\" {\n  forward_to = [loki.write.w.receiver]\n  rule {\n    action      = \"labelmap\"\n    regex       = \"fil(e)name\"\n" +
			"    replacement = \"__t$1nant_id__\"\n  }\n}\n" + lokiWriter(`    tenant_id = "mine"`),
		"a-groups": lokiSource("a-groups", "loki.process.p.receiver") +
			"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.regex {\n    expression         = `^(?P<first>\\S+)`\n" +
			"    labels_from_groups = true\n  }\n}\n" + lokiWriter(`    tenant_id = "mine"`),
		// otelcol.exporter.loki would label each entry __tenant_id__=victim;
		// the strict served form routes it through a drop.
		"a-otlp-loki": otlpRoundTrip("a-otlp-loki", "otelcol.exporter.loki.e.input") +
			"otelcol.exporter.loki \"e\" {\n  forward_to = [loki.write.w.receiver]\n}\n" + lokiWriter(`    tenant_id = "mine"`),
		"a-secretfilter": lokiSource("a-secretfilter", "loki.secretfilter.s.receiver") +
			"loki.secretfilter \"s\" {\n  forward_to   = [loki.write.w.receiver]\n  origin_label = \"job\"\n}\n" + lokiWriter(`    tenant_id = "mine"`),
	}
}

// refusedCase is a pipeline strict mode refuses, with what its refusal says.
type refusedCase struct {
	contents string
	want     string
}

func refusedCases() map[string]refusedCase {
	return map[string]refusedCase{
		"r-label-map": {
			lokiSource("r-label-map", "loki.process.p.receiver") +
				"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n" +
				"    values = encoding.from_json(\"{\\\"__tenant_id__\\\":\\\"victim\\\"}\")\n  }\n}\n" + lokiWriter(""),
			"is not a literal map",
		},
		"r-header": {
			lokiSource("r-header", toWriter) + lokiWriter(`    headers = { "X-Scope-OrgID" = "victim" }`),
			`tenant "victim" is not one this organisation owns`,
		},
		"r-declare": {
			outDeclare + "out \"o\" {\n  tenant = \"victim\"\n}\n" + lokiSource("r-declare", "out.o.receiver"),
			`tenant "victim" is not one this organisation owns`,
		},
		"r-foreach": {
			outDeclare + "foreach \"f\" {\n  collection = [\"victim\"]\n  var        = \"t\"\n\n  template {\n" +
				"    out \"o\" {\n      tenant = t\n    }\n" + indent(lokiSource("r-foreach", "out.o.receiver")) + "  }\n}\n",
			"cannot be verified",
		},
		"r-groups-regex": {
			lokiSource("r-groups-regex", "loki.process.p.receiver") +
				"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.regex {\n" +
				"    expression         = `^(?P<__tenant_id__>\\S+)`\n    labels_from_groups = true\n  }\n}\n" + lokiWriter(""),
			"labels_from_groups",
		},
		"r-groups-pattern": {
			lokiSource("r-groups-pattern", "loki.process.p.receiver") +
				"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.pattern {\n" +
				"    pattern            = \"<<__tenant_id__> <_>\"\n    labels_from_groups = true\n  }\n}\n" + lokiWriter(""),
			"labels_from_groups",
		},
		"r-relabel": {
			lokiSource("r-relabel", "loki.relabel.r.receiver") +
				"loki.relabel \"r\" {\n  forward_to = [loki.write.w.receiver]\n  rule {\n    source_labels = [\"filename\"]\n" +
				"    target_label  = \"__tenant_id__\"\n  }\n}\n" + lokiWriter(""),
			"from its source labels",
		},
		"r-exporter-loki": {
			otlpRoundTrip("r-exporter-loki", "conv.c.input") +
				"declare \"conv\" {\n  argument \"fwd\" { }\n\n  otelcol.exporter.loki \"e\" {\n    forward_to = argument.fwd.value\n  }\n\n" +
				"  export \"input\" {\n    value = otelcol.exporter.loki.e.input\n  }\n}\n\nconv \"c\" {\n  fwd = [loki.write.w.receiver]\n}\n" +
				lokiWriter(`    tenant_id = "mine"`),
			"forward_to is not a list of component receivers",
		},
		// stage.truncate rewrites the owned tenant "mine" to "mix".
		"r-truncate": {
			lokiSource("r-truncate", "loki.process.p.receiver") +
				"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n" +
				"    values = { \"__tenant_id__\" = \"mine\" }\n  }\n  stage.truncate {\n    rule {\n      limit       = \"3B\"\n" +
				"      suffix      = \"x\"\n      source_type = \"label\"\n      sources     = [\"__tenant_id__\"]\n    }\n  }\n}\n" +
				lokiWriter(""),
			"truncates __tenant_id__",
		},
		"r-secretfilter": {
			lokiSource("r-secretfilter", "loki.secretfilter.s.receiver") +
				"loki.secretfilter \"s\" {\n  forward_to   = [loki.write.w.receiver]\n  origin_label = \"__tenant_id__\"\n}\n" +
				lokiWriter(`    tenant_id = "mine"`),
			"origin_label is __tenant_id__",
		},
		"r-refused-component": {
			"otelcol.auth.google \"g\" { }\n" + lokiSource("r-refused-component", toWriter) +
				lokiWriter(`    headers = { "X-Scope-OrgID" = "victim" }`),
			"strict tenant mode accepts only classified components",
		},
		"r-unclassified-component": {
			"loki.source.future \"f\" {\n  forward_to = [loki.write.w.receiver]\n}\n" +
				lokiWriter(`    tenant_id = "victim"`),
			"strict tenant mode accepts only classified components",
		},
	}
}

// adminAllowed is the component the suite allows for the org; adminAllowedCase
// uses it with an X-Scope-OrgID header the org does not own in its own
// attributes: the component refusal is lifted, every other check still
// applies. (No shipped schema has it, so a real Alloy could not run it; the
// case is refused at save and, stored directly, excluded, so none ever does.)
const adminAllowed = "otelcol.exporter.future"

var adminAllowedCase = refusedCase{
	adminAllowed + " \"f\" {\n  client {\n    endpoint = \"http://tenantsink:3100\"\n" +
		"    headers  = { \"X-Scope-OrgID\" = \"victim\" }\n  }\n}\n",
	`tenant "victim" is not one this organisation owns`,
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "    " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// sinkRecord is one push tenantsink recorded.
type sinkRecord struct {
	Path   string              `json:"path"`
	Tenant string              `json:"tenant"`
	Labels []map[string]string `json:"labels"`
	Error  string              `json:"error"`
}

func sinkRecords() []sinkRecord {
	GinkgoHelper()
	resp, err := http.Get(sinkURL + "/records")
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	var out []sinkRecord
	Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
	return out
}

// tenantsByCase is, for every case label the sink saw, the tenants its
// entries arrived under.
func tenantsByCase(recs []sinkRecord) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, r := range recs {
		for _, ls := range r.Labels {
			c := ls["case"]
			if seen[c] == nil {
				seen[c] = map[string]bool{}
			}
			seen[c][r.Tenant] = true
		}
	}
	out := map[string][]string{}
	for c, ts := range seen {
		for t := range ts {
			out[c] = append(out[c], t)
		}
		sort.Strings(out[c])
	}
	return out
}

// psql runs sql in the e2e database and returns its unaligned output.
func psql(sql string) string {
	GinkgoHelper()
	cmd := exec.Command("docker", "compose", "-f", "docker-compose.e2e.yaml", "--profile", "strict", "exec", "-T", "postgres",
		"psql", "-U", "shepherd", "-d", "shepherd_e2e", "-v", "ON_ERROR_STOP=1", "-tA", "-f", "-")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "psql: %s", out)
	return strings.TrimSpace(string(out))
}

// dollarQuote is s as a Postgres dollar-quoted literal.
func dollarQuote(s string) string {
	const tag = "$shepherd_e2e$"
	Expect(s).NotTo(ContainSubstring(tag))
	return tag + s + tag
}

var _ = Describe("Strict tenant mode against a real Alloy (#421)", Ordered, Label("strict-tenant"), func() {
	var (
		admin *apiClient
		orgID string
	)
	validateDiags := func(contents string) []string {
		GinkgoHelper()
		var out struct {
			Valid       bool `json:"valid"`
			Diagnostics []struct {
				Message string `json:"message"`
			} `json:"diagnostics"`
		}
		admin.mustRPC("PipelineService", "ValidatePipeline", map[string]any{"org_id": orgID, "name": "probe", "contents": contents}, &out)
		var msgs []string
		for _, d := range out.Diagnostics {
			msgs = append(msgs, d.Message)
		}
		return msgs
	}
	create := func(name, contents string) int {
		GinkgoHelper()
		var out struct {
			ID string `json:"id"`
		}
		code := admin.rpc("PipelineService", "CreatePipeline", map[string]any{
			"org_id": orgID, "name": name, "contents": contents, "matchers": []string{`cluster="` + strictCluster + `"`},
		}, &out)
		if code == http.StatusOK {
			admin.mustRPC("PipelineService", "EnablePipeline", map[string]any{"org_id": orgID, "id": out.ID}, nil)
		}
		return code
	}
	served := func() string {
		GinkgoHelper()
		return psql(`SELECT sc.content FROM serve_cache sc JOIN collectors c ON c.id = sc.collector_id
JOIN clusters cl ON cl.id = c.cluster_id WHERE cl.name = '` + strictCluster + `' AND NOT sc.dirty;`)
	}

	BeforeAll(func() {
		waitHTTP(shepherdURL+"/healthz", 2*time.Minute)
		waitHTTP(sinkURL+"/healthz", time.Minute)
		admin = newAdminClient()
		var orgs struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		}
		admin.mustRPC("AdminService", "ListOrgs", nil, &orgs)
		for _, o := range orgs.Items {
			if o.Name == strictOrgName {
				orgID = o.ID
			}
		}
		Expect(orgID).NotTo(BeEmpty(), "the strict profile's org %q must exist", strictOrgName)
		admin.mustRPC("AdminService", "AddOrgTenant", map[string]any{
			"org_id": orgID, "tenant": "mine", "kind": "ORG_TENANT_KIND_EXACT", "make_default": true,
		}, nil)
		admin.mustRPC("AdminService", "SetOrgStrictTenants", map[string]any{"org_id": orgID, "strict_tenants": true}, nil)
		admin.mustRPC("AdminService", "AddOrgStrictComponent", map[string]any{"org_id": orgID, "component": adminAllowed}, nil)
		// The collector has registered (its first poll claims the cluster).
		Eventually(func() string {
			return psql(`SELECT count(*) FROM collectors c JOIN clusters cl ON cl.id = c.cluster_id WHERE cl.name = '` + strictCluster + `';`)
		}, 2*time.Minute, 2*time.Second).Should(Equal("1"))
	})

	It("refuses every refused construct at save, an admin-allowed component's included", func() {
		cases := refusedCases()
		cases["r-admin-allowed"] = adminAllowedCase
		for name, c := range cases {
			Expect(validateDiags(c.contents)).To(ContainElement(ContainSubstring(c.want)), name)
			Expect(create(name, c.contents)).To(Equal(http.StatusBadRequest), name)
		}
		Expect(validateDiags(adminAllowedCase.contents)).NotTo(ContainElement(ContainSubstring("accepts only classified components")),
			"an admin-allowed component lifts its own refusal")
	})

	It("excludes each refused construct stored directly from the served config", func() {
		cases := refusedCases()
		cases["r-admin-allowed"] = adminAllowedCase
		var sql strings.Builder
		for name, c := range cases {
			fmt.Fprintf(&sql, `INSERT INTO pipelines (org_id, name, contents, matchers, enabled, source, wizard_state, created_by, updated_by)
VALUES ('%s', '%s', %s, '["cluster=\"%s\""]'::jsonb, true, 'ui', '{}'::jsonb, 'e2e', 'e2e');
`, orgID, name, dollarQuote(c.contents), strictCluster)
		}
		psql(sql.String())
		// Saving the accepted pipelines marks the org's served configs
		// dirty and recomputes them, the stored ones included.
		for name, contents := range acceptedCases() {
			Expect(create(name, contents)).To(Equal(http.StatusOK), "%s: %v", name, validateDiags(contents))
		}
		Eventually(served, 2*time.Minute, 2*time.Second).Should(And(
			ContainSubstring(fmt.Sprintf("// Excluded (%d) - strict tenant check:", len(cases))),
			ContainSubstring(`declare "pipe_a_otlp_loki"`)))
		content := served()
		for name := range cases {
			Expect(content).To(ContainSubstring("//   - "+name+": "), name)
			Expect(content).NotTo(ContainSubstring(`declare "pipe_`+strings.ReplaceAll(name, "-", "_")+`"`), name)
		}
		Expect(content).To(ContainSubstring(`stage.label_drop`), "the served form drops __tenant_id__ after otelcol.exporter.loki")
		Expect(content).To(ContainSubstring(`regex  = "__tenant_id__"`), "and after the labelmap")
	})

	It("delivers every accepted construct under tenant mine, and nothing under any other tenant", func() {
		want := map[string][]string{}
		for name := range acceptedCases() {
			want[name] = []string{"mine"}
		}
		Eventually(func() map[string][]string { return tenantsByCase(sinkRecords()) }, 3*time.Minute, 2*time.Second).
			Should(Equal(want))
		Consistently(func() map[string][]string { return tenantsByCase(sinkRecords()) }, 20*time.Second, 2*time.Second).
			Should(Equal(want))
		for _, r := range sinkRecords() {
			Expect(r.Error).To(BeEmpty(), "the sink decodes every push")
			Expect(r.Tenant).To(Equal("mine"), "no push under another tenant: %+v", r)
			for _, ls := range r.Labels {
				Expect(ls).NotTo(HaveKey("__tenant_id__"))
			}
		}
	})

	// The control: the sink and the Alloy under test do show another tenant
	// when nothing excludes the stored pipelines. With strict mode off they
	// are served as stored (the three a real Alloy cannot run disabled
	// first), and their entries arrive under the tenants they set.
	It("a control: with strict mode off, the same stored pipelines reach the sink under other tenants", func() {
		for _, name := range []string{"r-refused-component", "r-unclassified-component", "r-admin-allowed"} {
			id := psql(`SELECT id FROM pipelines WHERE org_id = '` + orgID + `' AND name = '` + name + `';`)
			admin.mustRPC("PipelineService", "DisablePipeline", map[string]any{"org_id": orgID, "id": id}, nil)
		}
		admin.mustRPC("AdminService", "SetOrgStrictTenants", map[string]any{"org_id": orgID, "strict_tenants": false}, nil)
		Eventually(func() map[string][]string { return tenantsByCase(sinkRecords()) }, 3*time.Minute, 2*time.Second).
			Should(And(
				HaveKeyWithValue("r-header", []string{"victim"}),
				HaveKeyWithValue("r-declare", []string{"victim"}),
				HaveKeyWithValue("r-foreach", []string{"victim"}),
				HaveKeyWithValue("r-label-map", []string{"victim"}),
				HaveKeyWithValue("r-exporter-loki", []string{"victim"}),
				HaveKeyWithValue("r-relabel", []string{tailFile}),
				HaveKeyWithValue("r-groups-regex", ContainElement("remotecfg")),
				HaveKeyWithValue("r-truncate", []string{"mix"}),
			))
	})
})
