package validate_test

import (
	"context"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/gateway"
	"shepherd/internal/validate"
)

// strictFor is the policy of an org owning the exact tenant "acme" and every
// tenant under the prefix "acme-".
func strictFor() validate.TenantPolicy {
	owned := []gateway.OrgTenant{{Tenant: "acme", Kind: gateway.TenantExact}, {Tenant: "acme-", Kind: gateway.TenantPrefix}}
	return validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return gateway.OrgOwnsTenant(owned, t) }}
}

func errorsOf(ds []validate.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		if !d.Warning {
			out = append(out, d.Message)
		}
	}
	return out
}

var _ = Describe("Strict tenant mode's pipeline lint (TenantLint)", func() {
	remoteWrite := func(tenant string) string {
		return `prometheus.remote_write "out" {
  endpoint {
    url = "https://mimir.example/api/v1/push"
    headers = {
      "X-Scope-OrgID" = ` + tenant + `,
    }
  }
}
`
	}

	It("refuses a literal X-Scope-OrgID header in prometheus.remote_write the org does not own", func() {
		ds := validate.TenantLint(remoteWrite(`"globex"`), strictFor())
		Expect(errorsOf(ds)).To(ConsistOf(ContainSubstring(`tenant "globex" is not one this organisation owns`)))
		Expect(ds[0].Line).To(Equal(5))
		Expect(ds[0].Stage).To(Equal(1))
	})

	It("accepts an owned exact tenant and one under an owned prefix", func() {
		Expect(validate.TenantLint(remoteWrite(`"acme"`), strictFor())).To(BeEmpty())
		Expect(validate.TenantLint(remoteWrite(`"acme-prod"`), strictFor())).To(BeEmpty())
	})

	It("matches the header name case-insensitively, quoted or not", func() {
		src := `loki.write "out" {
  endpoint {
    url = "https://loki.example/loki/api/v1/push"
    headers = { "x-scope-orgid" = "globex" }
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(1))
	})

	It("refuses each unowned tenant of a |-joined header value", func() {
		Expect(errorsOf(validate.TenantLint(remoteWrite(`"acme|globex"`), strictFor()))).
			To(ConsistOf(ContainSubstring(`"globex"`)))
	})

	It("refuses loki.write's literal tenant_id and accepts an owned one", func() {
		src := func(t string) string {
			return `loki.write "out" {
  endpoint {
    url = "https://loki.example/loki/api/v1/push"
    tenant_id = "` + t + `"
  }
}
`
		}
		Expect(errorsOf(validate.TenantLint(src("globex"), strictFor()))).
			To(ConsistOf(ContainSubstring("loki.write endpoint tenant_id")))
		Expect(validate.TenantLint(src("acme"), strictFor())).To(BeEmpty())
	})

	It("refuses the rules components' tenant_id and loki.process stage.tenant's value", func() {
		src := `mimir.rules.kubernetes "r" {
  address = "http://mimir:8080"
  tenant_id = "globex"
}
loki.rules.kubernetes "r" {
  address = "http://loki:3100"
  tenant_id = "globex"
}
loki.process "p" {
  forward_to = []
  stage.tenant {
    value = "globex"
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(3))
	})

	It("refuses an otelcol.auth.headers X-Scope-OrgID header the org does not own", func() {
		src := `otelcol.auth.headers "t" {
  header {
    key   = "X-Scope-OrgID"
    value = "globex"
  }
  header {
    key   = "Authorization"
    value = "Bearer x"
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("otelcol.auth.headers")))
	})

	It("checks headers inside otelcol exporters and declare blocks", func() {
		src := `declare "m" {
  otelcol.exporter.otlphttp "o" {
    client {
      endpoint = "https://tempo.example"
      headers  = { "X-Scope-OrgID" = "globex" }
    }
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(1))
	})

	It("refuses a tenant set from sys.env, a reference or a computed concatenation (cannot be verified)", func() {
		for _, expr := range []string{`sys.env("TENANT")`, `local.file.t.content`, `"ac" + sys.env("X")`} {
			Expect(errorsOf(validate.TenantLint(remoteWrite(expr), strictFor()))).To(ConsistOf(
				ContainSubstring("cannot be verified")), expr)
		}
	})

	It("refuses a headers map that is not an object literal and stage.tenant's label", func() {
		src := `prometheus.remote_write "out" {
  endpoint {
    url     = "https://mimir.example/api/v1/push"
    headers = local.h
  }
}
loki.process "p" {
  forward_to = []
  stage.tenant {
    label = "team"
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(2))
	})

	It("ignores Azure AD tenant_id attributes, which name an identity tenant", func() {
		src := `discovery.azure "a" {
  oauth {
    client_id     = "x"
    tenant_id     = "globex"
    client_secret = "y"
  }
}
`
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
	})

	It("finds nothing when the policy is not strict (a non-strict org is unchanged)", func() {
		Expect(validate.TenantLint(remoteWrite(`"globex"`), validate.TenantPolicy{})).To(BeEmpty())
	})

	It("finds nothing in text that does not parse (Stage 1 reports it)", func() {
		Expect(validate.TenantLint(`prometheus.remote_write "x" {`, strictFor())).To(BeEmpty())
	})
	// Review fixes (PR #388).
	Describe("http_headers (lists of values)", func() {
		DescribeTable("refuses an unowned tenant in any element",
			func(src string) {
				Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring(`"victim"`)))
			},
			Entry("prometheus.remote_write endpoint", `prometheus.remote_write "o" {
  endpoint {
    url          = "https://m.example/push"
    http_headers = { "X-Scope-OrgID" = ["acme", "victim"] }
  }
}
`),
			Entry("loki.write endpoint whose own tenant_id is owned", `loki.write "o" {
  endpoint {
    url          = "https://l.example/push"
    tenant_id    = "acme"
    http_headers = { "x-scope-orgid" = ["victim"] }
  }
}
`),
			Entry("mimir.rules.kubernetes top level", `mimir.rules.kubernetes "r" {
  address      = "http://mimir:8080"
  http_headers = { "X-Scope-OrgID" = ["victim"] }
}
`),
		)

		It("leaves CORS header lists alone", func() {
			src := `otelcol.receiver.otlp "r" {
  http {
    cors {
      allowed_headers = ["X-Scope-OrgID"]
    }
  }
  output {}
}
`
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
		})
	})

	Describe("Loki's reserved __tenant_id__ label (overrides loki.write's tenant_id)", func() {
		DescribeTable("refuses a literal unowned value",
			func(src string) {
				Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring(`"victim"`)))
			},
			Entry("stage.static_labels", `loki.process "p" {
  forward_to = []
  stage.static_labels {
    values = { "__tenant_id__" = "victim" }
  }
}
`),
			Entry("a loki.relabel rule", `loki.relabel "r" {
  forward_to = []
  rule {
    target_label = "__tenant_id__"
    replacement  = "victim"
  }
}
`),
			Entry("a discovery.relabel rule", `discovery.relabel "r" {
  targets = []
  rule {
    action       = "replace"
    target_label = "__tenant_id__"
    replacement  = "victim"
  }
}
`),
			Entry("a loki.source labels map", `loki.source.api "a" {
  http {}
  forward_to = []
  labels     = { "__tenant_id__" = "victim" }
}
`),
		)

		It("accepts an owned one", func() {
			src := `loki.process "p" {
  forward_to = []
  stage.static_labels {
    values = { "__tenant_id__" = "acme-logs" }
  }
}
`
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
		})

		It("refuses a value taken from the data: stage.labels, a regex replacement, no replacement", func() {
			src := `loki.process "p" {
  forward_to = []
  stage.labels {
    values = { "__tenant_id__" = "team" }
  }
}
loki.relabel "r" {
  forward_to = []
  rule {
    source_labels = ["team"]
    target_label  = "__tenant_id__"
    replacement   = "$1"
  }
  rule {
    target_label = "__tenant_id__"
  }
}
`
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(3))
		})
	})

	It("finds stage.tenant nested under stage.match, at any depth", func() {
		src := `loki.process "p" {
  forward_to = []
  stage.match {
    selector = "{app=\"x\"}"
    stage.match {
      selector = "{env=\"y\"}"
      stage.tenant {
        value = "victim"
      }
    }
  }
}
`
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring(`"victim"`)))
	})

	Describe("values it can work out", func() {
		It("folds a concatenation of string literals", func() {
			Expect(errorsOf(validate.TenantLint(remoteWrite(`"vic" + "tim"`), strictFor()))).
				To(ConsistOf(ContainSubstring(`"victim"`)))
			Expect(validate.TenantLint(remoteWrite(`("ac" + "me") + "-x"`), strictFor())).To(BeEmpty())
		})

		It("checks a declare argument's literal default and every literal it is given", func() {
			src := `declare "w" {
  argument "tenant" {
    optional = true
    default  = "victim"
  }
  prometheus.remote_write "o" {
    endpoint {
      url     = "https://m.example/push"
      headers = { "X-Scope-OrgID" = argument.tenant.value }
    }
  }
}
w "a" { tenant = "acme" }
w "b" { tenant = "other" }
`
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(
				ContainSubstring(`"victim"`), ContainSubstring(`"other"`)))
		})

		It("refuses an argument it cannot read (no default, never given)", func() {
			src := `declare "w" {
  argument "tenant" { }
  prometheus.remote_write "o" {
    endpoint {
      url     = "https://m.example/push"
      headers = { "X-Scope-OrgID" = argument.tenant.value }
    }
  }
}
`
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(HaveLen(1))
		})
	})

	// Second review (PR #388): strict mode refuses what it cannot verify.
	Describe("modules", func() {
		module := func(tenant string) string {
			return "declare \"w\" {\n  argument \"tenant\" {\n    optional = true\n    default  = \"acme\"\n  }\n" +
				"  prometheus.remote_write \"o\" {\n    endpoint {\n      url     = \"https://m.example/push\"\n" +
				"      headers = { \"X-Scope-OrgID\" = argument.tenant.value }\n    }\n  }\n}\n" +
				"loki.write \"l\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"" + tenant + "\"\n  }\n}\n"
		}
		importString := func(content string) string {
			return "import.string \"m\" {\n  content = " + strconv.Quote(content) + "\n}\n"
		}

		It("lints an import.string module's literal content with the same rules", func() {
			Expect(errorsOf(validate.TenantLint(importString(module("victim")), strictFor()))).
				To(ConsistOf(And(ContainSubstring(`import.string "m"`), ContainSubstring(`"victim"`))))
			Expect(validate.TenantLint(importString(module("acme")), strictFor())).To(BeEmpty())
		})

		It("checks what the importing text gives the module's declares", func() {
			src := importString(module("acme")) + "m.w \"x\" {\n  tenant = \"victim\"\n}\n"
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring(`"victim"`)))
		})

		It("refuses every other import, and import.string content it cannot read", func() {
			for _, src := range []string{
				"import.git \"g\" {\n  repository = \"https://example.com/m.git\"\n  path = \"m.alloy\"\n}\n",
				"import.http \"h\" {\n  url = \"https://example.com/m.alloy\"\n}\n",
				"import.file \"f\" {\n  filename = \"/etc/m.alloy\"\n}\n",
				"import.string \"s\" {\n  content = local.file.m.content\n}\n",
			} {
				Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("cannot be verified")), src)
			}
		})

		It("follows a value passed down two declare levels", func() {
			src := `declare "w" {
  argument "tenant" { }
  prometheus.remote_write "o" {
    endpoint {
      url     = "https://m.example/push"
      headers = { "X-Scope-OrgID" = argument.tenant.value }
    }
  }
}
declare "outer" {
  argument "t" { }
  w "inner" { tenant = argument.t.value }
}
outer "o" { t = "victim" }
`
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring(`"victim"`)))
		})
	})

	Describe("otelcol.auth.bearer", func() {
		bearer := func(attrs string) string {
			return "otelcol.auth.bearer \"b\" {\n" + attrs + "}\n"
		}
		It("reads the token as the tenant when the header is X-Scope-OrgID and the scheme is empty", func() {
			Expect(errorsOf(validate.TenantLint(bearer("  header = \"x-scope-orgid\"\n  scheme = \"\"\n  token = \"victim\"\n"), strictFor()))).
				To(ConsistOf(ContainSubstring(`"victim"`)))
			Expect(validate.TenantLint(bearer("  header = \"X-Scope-OrgID\"\n  scheme = \"\"\n  token = \"acme\"\n"), strictFor())).To(BeEmpty())
		})
		It("refuses a tenant-header bearer with a scheme or a computed token, and a computed header", func() {
			for _, attrs := range []string{
				"  header = \"X-Scope-OrgID\"\n  token = \"acme\"\n",
				"  header = \"X-Scope-OrgID\"\n  scheme = \"Bearer\"\n  token = \"acme\"\n",
				"  header = \"X-Scope-OrgID\"\n  scheme = \"\"\n  token = sys.env(\"T\")\n",
				"  header = sys.env(\"H\")\n  token = \"acme\"\n",
			} {
				Expect(errorsOf(validate.TenantLint(bearer(attrs), strictFor()))).To(HaveLen(1), attrs)
			}
		})
		It("leaves an Authorization bearer alone", func() {
			Expect(validate.TenantLint(bearer("  token = sys.env(\"T\")\n"), strictFor())).To(BeEmpty())
		})
	})

	Describe("relabel target_label", func() {
		rule := func(body string) string {
			return "loki.relabel \"r\" {\n  forward_to = []\n  rule {\n" + body + "  }\n}\n"
		}
		It("resolves a computed target_label", func() {
			Expect(errorsOf(validate.TenantLint(rule("    target_label = \"__tenant\" + \"_id__\"\n    replacement = \"victim\"\n"), strictFor()))).
				To(ConsistOf(ContainSubstring(`"victim"`)))
		})
		It("refuses a target_label or action it cannot read, and a labelmap naming __tenant_id__", func() {
			for _, body := range []string{
				"    target_label = sys.env(\"L\")\n    replacement = \"acme\"\n",
				"    action = sys.env(\"A\")\n    target_label = \"team\"\n",
				"    action = \"labelmap\"\n    regex = \"team\"\n    replacement = \"__tenant_id__\"\n",
				"    action = \"lowercase\"\n    source_labels = [\"t\"]\n    target_label = \"__tenant_id__\"\n",
			} {
				Expect(errorsOf(validate.TenantLint(rule(body), strictFor()))).To(HaveLen(1), body)
			}
		})
		It("leaves an ordinary rule alone", func() {
			Expect(validate.TenantLint(rule("    source_labels = [\"a\"]\n    target_label = \"team\"\n"), strictFor())).To(BeEmpty())
		})
	})

	It("lists the literal tenants a pipeline writes to (TenantLiterals), and whether that is all of them", func() {
		lits, complete, err := validate.TenantLiterals(remoteWrite(`"acme|victim"`))
		Expect(err).NotTo(HaveOccurred())
		Expect(lits).To(ConsistOf("acme", "victim"))
		Expect(complete).To(BeTrue())
		lits, complete, err = validate.TenantLiterals(remoteWrite(`sys.env("T")`))
		Expect(err).NotTo(HaveOccurred())
		Expect(lits).To(BeEmpty())
		Expect(complete).To(BeFalse())
	})

	// Third review (PR #388).
	Describe("relabel expansion", func() {
		rule := func(body string) string {
			return "loki.relabel \"r\" {\n  forward_to = []\n  rule {\n" + body + "  }\n}\n"
		}
		It("refuses a replace target_label that expands a regex reference", func() {
			Expect(errorsOf(validate.TenantLint(rule("    source_labels = [\"t\"]\n    regex = \"(.*)\"\n    target_label = \"__tenant_${1}__\"\n    replacement = \"acme\"\n"), strictFor()))).
				To(HaveLen(1))
		})
		It("refuses a labelmap unless its replacement is a literal without $ that is not __tenant_id__", func() {
			for _, body := range []string{
				"    action = \"labelmap\"\n    regex = \"x_(.*)\"\n",
				"    action = \"labelmap\"\n    regex = \"x_(.*)\"\n    replacement = \"$1\"\n",
				"    action = \"labelmap\"\n    regex = \"x\"\n    replacement = sys.env(\"R\")\n",
			} {
				Expect(errorsOf(validate.TenantLint(rule(body), strictFor()))).To(HaveLen(1), body)
			}
			Expect(validate.TenantLint(rule("    action = \"labelmap\"\n    regex = \"x_team\"\n    replacement = \"team\"\n"), strictFor())).To(BeEmpty())
		})
	})

	Describe("push sources that forward the tenant a client sends", func() {
		src := func(source, between string) string {
			return source + " \"in\" {\n  http { }\n  forward_to = [" + between + "]\n}\n" +
				"loki.process \"p\" {\n  forward_to = [loki.write.w.receiver]\n  stage.static_labels {\n    values = { \"__tenant_id__\" = \"acme\" }\n  }\n}\n" +
				"loki.process \"drop\" {\n  forward_to = [loki.write.w.receiver]\n  stage.label_drop {\n    values = [\"__tenant_id__\"]\n  }\n}\n" +
				"loki.process \"noop\" {\n  forward_to = [loki.write.w.receiver]\n  stage.match {\n    selector = \"{a=\\\"b\\\"}\"\n    stage.static_labels {\n      values = { \"__tenant_id__\" = \"acme\" }\n    }\n  }\n}\n" +
				"loki.relabel \"r\" {\n  forward_to = [loki.write.w.receiver]\n  rule {\n    target_label = \"__tenant_id__\"\n    replacement  = \"acme\"\n  }\n}\n" +
				"loki.write \"w\" {\n  endpoint {\n    url = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n}\n"
		}
		DescribeTable("refuses a path to a writer that keeps the client's __tenant_id__",
			func(source, between string) {
				Expect(errorsOf(validate.TenantLint(src(source, between), strictFor()))).To(ConsistOf(ContainSubstring("client")))
			},
			Entry("loki.source.api straight to the writer", "loki.source.api", "loki.write.w.receiver"),
			Entry("a conditional overwrite only", "loki.source.api", "loki.process.noop.receiver"),
			Entry("one safe path and one unsafe", "loki.source.heroku", "loki.process.p.receiver, loki.write.w.receiver"),
			Entry("loki.source.awsfirehose", "loki.source.awsfirehose", "loki.write.w.receiver"),
			Entry("a component it cannot follow", "loki.source.api", "argument.out.value"),
		)
		DescribeTable("accepts every path overwriting or dropping it first",
			func(between string) {
				Expect(validate.TenantLint(src("loki.source.api", between), strictFor())).To(BeEmpty())
			},
			Entry("stage.static_labels", "loki.process.p.receiver"),
			Entry("stage.label_drop", "loki.process.drop.receiver"),
			Entry("an unconditional relabel replace", "loki.relabel.r.receiver"),
			Entry("two safe paths", "loki.process.p.receiver, loki.relabel.r.receiver"),
		)
	})

	It("keys declares by scope: a nested declare's default does not replace another's", func() {
		src := `declare "w" {
  argument "tenant" {
    optional = true
    default  = "acme"
  }
  prometheus.remote_write "o" {
    endpoint {
      url     = "https://m.example/push"
      headers = { "X-Scope-OrgID" = argument.tenant.value }
    }
  }
}
declare "other" {
  declare "w" {
    argument "tenant" {
      optional = true
      default  = "acme"
    }
  }
  w "inner" { tenant = "victim" }
}
w "top" { }
`
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty(), "victim goes to other's own w, which writes nothing")
		Expect(errorsOf(validate.TenantLint(strings.Replace(src, `w "top" { }`, `w "top" { tenant = "victim" }`, 1), strictFor()))).
			To(ConsistOf(ContainSubstring(`"victim"`)))
	})

	It("refuses an import.string whose content does not parse", func() {
		Expect(errorsOf(validate.TenantLint("import.string \"m\" {\n  content = \"declare {{{\"\n}\n", strictFor()))).
			To(ConsistOf(ContainSubstring("does not parse")))
	})
})

var _ = Describe("The gate with strict tenant mode (ValidatePipeline)", func() {
	src := `prometheus.remote_write "out" {
  endpoint {
    url = "https://mimir.example/api/v1/push"
    headers = { "X-Scope-OrgID" = "globex" }
  }
}
`
	It("fails a pipeline the lint refuses, positioned in the text the user wrote", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p", src, strictFor())
		Expect(r.Valid).To(BeFalse())
		Expect(positions(r.Diagnostics)).To(ContainElement(HavePrefix("4:35 ")))
	})

	It("fails a tenant it cannot verify", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p",
			strings.Replace(src, `"globex"`, `sys.env("T")`, 1), strictFor())
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
	})

	It("is exactly Stages 1-2 for an org not in strict mode", func() {
		r := noBinary().ValidatePipeline(context.Background(), "p", src, validate.TenantPolicy{})
		Expect(r.Valid).To(BeTrue())
		Expect(r.Diagnostics).To(BeEmpty())
	})
})
