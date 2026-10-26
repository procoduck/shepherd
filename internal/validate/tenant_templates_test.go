package validate_test

import (
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
)

// template wraps text in a foreach template.
func template(inner string) string {
	return "foreach \"f\" {\n  collection = [1]\n  var = \"x\"\n  template {\n" + indent(indent(inner)) + "  }\n}\n"
}

var _ = Describe("strict tenant mode reads declare arguments given inside a foreach template as unknown", func() {
	declare := func(body string) string {
		return "declare \"m\" {\n  argument \"v\" {\n    optional = true\n    default  = \"acme\"\n  }\n" + indent(body) + "}\n"
	}
	cases := map[string]struct{ body, given string }{
		"a tenant_id": {
			"loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = argument.v.value\n  }\n}\n",
			`"acme"`,
		},
		"a header": {
			"prometheus.remote_write \"o\" {\n  endpoint {\n    url     = \"https://m.example/push\"\n    headers = { \"X-Scope-OrgID\" = argument.v.value }\n  }\n}\n",
			`"acme"`,
		},
		"a label map": {
			"loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n  external_labels = argument.v.value\n}\n",
			`{ team = "a" }`,
		},
	}

	for name, c := range cases {
		It("refuses "+name+" whose argument is given from a template", func() {
			body := c.body
			if name == "a label map" {
				body = strings.Replace(body, "argument.v.value", "argument.l.value", 1)
				body = "argument \"l\" {\n  optional = true\n  default  = { team = \"a\" }\n}\n" + body
			}
			arg := "v"
			if name == "a label map" {
				arg = "l"
			}
			src := declare(body) + template("m \"a\" {\n  "+arg+" = "+c.given+"\n}\n")
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).NotTo(BeEmpty(), src)
			// The same instantiation at the top level is read.
			top := declare(body) + "m \"a\" {\n  " + arg + " = " + c.given + "\n}\n"
			Expect(validate.TenantLint(top, strictFor())).To(BeEmpty(), top)
		})
	}

	It("refuses a module argument given from a template", func() {
		mod := declare("loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = argument.v.value\n  }\n}\n")
		src := "import.string \"mod\" {\n  content = " + strconv.Quote(mod) + "\n}\n" + template("mod.m \"a\" {\n  v = \"acme\"\n}\n")
		Expect(errorsOf(validate.TenantLint(src, strictFor()))).NotTo(BeEmpty())
		top := "import.string \"mod\" {\n  content = " + strconv.Quote(mod) + "\n}\nmod.m \"a\" {\n  v = \"acme\"\n}\n"
		Expect(validate.TenantLint(top, strictFor())).To(BeEmpty())
	})
})

var _ = Describe("strict tenant mode checks label names taken from capture groups", func() {
	stage := func(name, attr, expr, groups string) string {
		return "loki.process \"p\" {\n  forward_to = []\n  " + name + " {\n    " + attr + " = " + expr + "\n" +
			"    labels_from_groups = " + groups + "\n  }\n}\n"
	}

	It("accepts groups whose names it reads, none of them __tenant_id__", func() {
		for _, src := range []string{
			stage("stage.regex", "expression", "`^(?P<level>\\w+) (?P<msg>.*)$`", "true"),
			stage("stage.pattern", "pattern", `"<level> <_> <msg>"`, "true"),
			stage("stage.regex", "expression", "`(?P<__tenant_id__>\\w+)`", "false"),
		} {
			Expect(validate.TenantLint(src, strictFor())).To(BeEmpty(), src)
		}
	})

	DescribeTable("refuses",
		func(src string) {
			Expect(errorsOf(validate.TenantLint(src, strictFor()))).To(ConsistOf(ContainSubstring("labels_from_groups")), src)
		},
		Entry("a regex group named __tenant_id__", stage("stage.regex", "expression", "`(?P<__tenant_id__>\\w+)`", "true")),
		Entry("the <name> group spelling", stage("stage.regex", "expression", "`(?<__tenant_id__>\\w+)`", "true")),
		Entry("a pattern capture named __tenant_id__", stage("stage.pattern", "pattern", `"<__tenant_id__> <_>"`, "true")),
		Entry("a regex that does not compile", stage("stage.regex", "expression", "`(?P<a>`", "true")),
		Entry("a computed expression", stage("stage.regex", "expression", `sys.env("RE")`, "true")),
		Entry("a computed pattern", stage("stage.pattern", "pattern", `sys.env("P")`, "true")),
		Entry("a computed labels_from_groups", stage("stage.regex", "expression", "`(?P<a>\\w+)`", `sys.env("G") == "1"`)),
	)
})

var _ = Describe("strict tenant mode checks loki.secretfilter's origin_label", func() {
	filter := func(label string) string {
		return "loki.secretfilter \"s\" {\n  forward_to   = []\n  origin_label = " + label + "\n}\n"
	}
	It("accepts a literal label other than __tenant_id__", func() {
		Expect(validate.TenantLint(filter(`"job"`), strictFor())).To(BeEmpty())
	})
	It("refuses __tenant_id__ and a value it cannot read", func() {
		for _, l := range []string{`"__tenant_id__"`, `sys.env("L")`} {
			Expect(errorsOf(validate.TenantLint(filter(l), strictFor()))).To(ConsistOf(ContainSubstring("origin_label")), l)
		}
	})
})

var _ = Describe("strict tenant mode and loki.source.podlogs", func() {
	It("accepts preserve_discovered_labels: the source drops every reserved label but __meta_ ones", func() {
		src := "loki.source.podlogs \"p\" {\n  forward_to                 = []\n  preserve_discovered_labels = true\n}\n"
		Expect(validate.TenantLint(src, strictFor())).To(BeEmpty())
	})
})
