package validate_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// redactionPipeline uses otelcol.processor.redaction, which the v1.18.1
// schema does not have (it first ships in v1.19.x) — issue #375's first
// lab observation.
const redactionPipeline = `otelcol.processor.redaction "scrub" {
  allow_all_keys = true
  output {
    traces = []
  }
}
`

// podMonitorPipeline sets scrape_classic_histograms in the scrape block of
// prometheus.operator.podmonitors — an attribute the v1.18.1 schema lacks,
// which made a 1.18.1 collector reject the whole remote config (#375).
const podMonitorPipeline = `prometheus.operator.podmonitors "pods" {
  forward_to = []
  scrape {
    scrape_classic_histograms = true
  }
}
`

func versionErrors(diags []validate.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if !d.Warning {
			out = append(out, d.Message)
		}
	}
	return out
}

func versionWarnings(diags []validate.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		if d.Warning {
			out = append(out, d.Message)
		}
	}
	return out
}

var pinned = validate.DisplayVersion(version.AlloySchemaVersion)

var _ = Describe("CheckAlloyVersions (#375)", func() {
	It("ships a schema for the pinned version and every released patch back to v1.18.0", func() {
		Expect(validate.ShippedVersions()).To(Equal([]string{
			"alloy-v1.18.0", "alloy-v1.18.1",
			"alloy-v1.19.0", "alloy-v1.19.1", "alloy-v1.19.2",
			"alloy-v1.20.0", "alloy-v1.20.1",
			"alloy-v1.21.0", "alloy-v1.21.1", "alloy-v1.21.2",
		}))
		Expect(validate.ShippedVersions()).To(ContainElement(version.AlloySchemaVersion))
	})

	// v1.21.0 added no component, but it added attributes. Before every patch
	// had its own schema a v1.21.0 collector was checked against v1.20.1 and
	// these were false refusals.
	It("checks a v1.21.0 collector against v1.21.0 itself, accepting what 1.21.0 added", func() {
		content := `loki.source.cloudflare "cf" {
  api_token  = "t"
  zone_id    = "z"
  api_url    = "https://api.cloudflare.example"
  forward_to = []
}
otelcol.encoding.jsonlog "j" {
  parse_ints = true
}
`
		rv := validate.ResolveVersion("v1.21.0")
		Expect(rv.Artifact).To(Equal("alloy-v1.21.0"))
		Expect(rv.How).To(Equal(validate.ResolvedExact))
		diags := validate.CheckAlloyVersions(content, []string{"v1.21.0"})
		Expect(versionErrors(diags)).To(BeEmpty())
		Expect(versionWarnings(diags)).NotTo(ContainElement(ContainSubstring("approximation")))
		// The release before refuses the same config, so the acceptance
		// above is v1.21.0's own schema talking.
		Expect(versionErrors(validate.CheckAlloyVersions(content, []string{"v1.20.1"}))).To(ConsistOf(
			`attribute api_url (in loki.source.cloudflare "cf") is not available in Alloy v1.20.1`,
			`attribute parse_ints (in otelcol.encoding.jsonlog "j") is not available in Alloy v1.20.1`,
		))
	})

	It("refuses otelcol.processor.redaction for a 1.18.1 collector, naming the version and component", func() {
		diags := validate.CheckAlloyVersions(redactionPipeline, []string{"v1.18.1"})
		errs := versionErrors(diags)
		Expect(errs).To(ConsistOf("component otelcol.processor.redaction is not available in Alloy v1.18.1"))
		Expect(diags[0].Line).To(Equal(1))
		Expect(diags[0].Stage).To(Equal(3))
	})

	It("accepts the same component for collectors whose schema has it", func() {
		Expect(versionErrors(validate.CheckAlloyVersions(redactionPipeline, []string{"v1.19.2", "1.20.1", pinned}))).To(BeEmpty())
	})

	It("refuses scrape_classic_histograms on prometheus.operator.* for a pre-1.19 collector", func() {
		errs := versionErrors(validate.CheckAlloyVersions(podMonitorPipeline, []string{"v1.18.1"}))
		Expect(errs).To(ConsistOf(
			`attribute scrape_classic_histograms (in prometheus.operator.podmonitors "pods" > scrape) is not available in Alloy v1.18.1`))
		Expect(versionErrors(validate.CheckAlloyVersions(podMonitorPipeline, []string{"v1.20.1"}))).To(BeEmpty())
	})

	It("checks a mixed-version collector against every version, refusing for the old one only", func() {
		errs := versionErrors(validate.CheckAlloyVersions(redactionPipeline, []string{pinned, "v1.18.1", "v1.20.1"}))
		Expect(errs).To(HaveLen(1))
		Expect(errs[0]).To(ContainSubstring("v1.18.1"))
	})

	It("checks components inside declare, foreach and import.string module text", func() {
		content := `declare "wrapped" {
  foreach "each" {
    collection = []
    var        = "x"
    template {
      otelcol.processor.redaction "a" {
        output {}
      }
    }
  }
}
import.string "mod" {
  content = "otelcol.processor.redaction \"b\" {\n  output {}\n}\n"
}
`
		errs := versionErrors(validate.CheckAlloyVersions(content, []string{"v1.18.1"}))
		Expect(errs).To(HaveLen(2))
	})

	It("never refuses an item the pinned schema does not know (that is Stage 2's call)", func() {
		content := `not.a.component "x" {
  anything = true
}
prometheus.operator.podmonitors "pods" {
  forward_to = []
  made_up_attribute = 1
}
`
		Expect(versionErrors(validate.CheckAlloyVersions(content, []string{"v1.18.1"}))).To(BeEmpty())
	})

	DescribeTable("resolves a version to its exact schema, else the nearest older one (owner decision on #375)",
		func(reported, want string, how validate.Resolution) {
			rv := validate.ResolveVersion(reported)
			Expect(rv.Artifact).To(Equal(want))
			Expect(rv.How).To(Equal(how))
		},
		Entry("exact", "v1.20.1", "alloy-v1.20.1", validate.ResolvedExact),
		Entry("exact without the v", "1.18.1", "alloy-v1.18.1", validate.ResolvedExact),
		Entry("every released patch is exact: v1.21.0", "v1.21.0", "alloy-v1.21.0", validate.ResolvedExact),
		Entry("every released patch is exact: v1.20.0", "v1.20.0", "alloy-v1.20.0", validate.ResolvedExact),
		Entry("every released patch is exact: v1.19.1 (a tag and an image, no GitHub release)", "v1.19.1", "alloy-v1.19.1", validate.ResolvedExact),
		Entry("an unreleased patch of a shipped minor: the minor's newest not newer", "v1.20.5", "alloy-v1.20.1", validate.ResolvedNearestOlder),
		Entry("an unreleased patch of an older minor: that minor's newest", "v1.19.3", "alloy-v1.19.2", validate.ResolvedNearestOlder),
		Entry("older than every shipped schema: the oldest", "v1.17.0", "alloy-v1.18.0", validate.ResolvedOldest),
		Entry("newer than every shipped schema: the pin", "v9.0.0", version.AlloySchemaVersion, validate.ResolvedNewer),
		Entry("not a release: the pin", "v1.21.0-rc.1", version.AlloySchemaVersion, validate.ResolvedNotRelease),
		Entry("devel: the pin", "devel", version.AlloySchemaVersion, validate.ResolvedNotRelease),
		Entry("none reported: the pin", "", version.AlloySchemaVersion, validate.ResolvedMissing),
	)

	It("checks a version without its own schema against the nearest older one, with an approximation warning", func() {
		// v1.18.5 resolves to v1.18.1, which lacks the attribute.
		diags := validate.CheckAlloyVersions(podMonitorPipeline, []string{"v1.18.5"})
		Expect(versionErrors(diags)).To(ConsistOf(ContainSubstring("not available in Alloy v1.18.1")))
		Expect(versionWarnings(diags)).To(ContainElement(
			"runs Alloy 1.18.5, which has no shipped schema; checked against the nearest older schema, v1.18.1 (an approximation)"))
	})

	It("warns for a version older than every shipped schema, checked against the oldest", func() {
		diags := validate.CheckAlloyVersions(redactionPipeline, []string{"v1.17.0"})
		Expect(versionErrors(diags)).To(ConsistOf(ContainSubstring("not available in Alloy v1.18.0")))
		Expect(versionWarnings(diags)).To(ContainElement(ContainSubstring("older than every shipped schema; checked against the oldest, v1.18.0")))
	})

	It("warns for a missing, unparseable, too-new or absent version", func() {
		Expect(versionWarnings(validate.CheckAlloyVersions(podMonitorPipeline, []string{""}))).To(ContainElement(
			"reports no Alloy version; checked against " + pinned + " only"))
		Expect(versionWarnings(validate.CheckAlloyVersions(podMonitorPipeline, []string{"devel"}))).To(ContainElement(
			`runs Alloy "devel", which is not a release version; checked against ` + pinned + " only"))
		Expect(versionWarnings(validate.CheckAlloyVersions(podMonitorPipeline, []string{"v9.0.0"}))).To(ContainElement(
			"runs Alloy 9.0.0, newer than every shipped schema; checked against " + pinned + " only"))
		Expect(versionWarnings(validate.CheckAlloyVersions(podMonitorPipeline, nil))).To(ContainElement(
			"has no active instance; checked against " + pinned + " only"))
		Expect(validate.VersionSetKey(nil)).NotTo(Equal(validate.VersionSetKey([]string{""})))
	})

	It("says nothing about resolution when the config has no component to check", func() {
		Expect(validate.CheckAlloyVersions("logging {\n  level = \"info\"\n}\n", []string{"v1.17.0"})).To(BeEmpty())
	})

	It("walks once per schema: many versions resolving to one schema give one finding and one capped, sanitised note", func() {
		var junk []string
		for i := range 50 {
			junk = append(junk, fmt.Sprintf("v9.9.%d", i))
		}
		junk = append(junk, "evil\n\"version\"<script>", strings.Repeat("x", 500))
		diags := validate.CheckAlloyVersions(podMonitorPipeline, append(junk, "v1.18.1", "1.18.1"))
		Expect(versionErrors(diags)).To(HaveLen(1), "one finding for the one schema lacking the item")
		warns := versionWarnings(diags)
		newer := 0
		for _, w := range warns {
			Expect(len(w)).To(BeNumerically("<", 300), "a note never echoes unbounded agent input")
			Expect(w).NotTo(ContainSubstring("\n"))
			Expect(w).NotTo(ContainSubstring("<script>"))
			if strings.Contains(w, "newer than every shipped schema") {
				newer++
				Expect(w).To(ContainSubstring("and 47 more"))
			}
		}
		Expect(newer).To(Equal(1), "fifty too-new versions are one note")
	})

	It("caps the distinct versions resolved, keeping the oldest release versions", func() {
		var many []string
		for i := range validate.MaxReportedVersions + 10 {
			many = append(many, fmt.Sprintf("v9.%d.0", i))
		}
		many = append(many, "v1.18.1")
		rv := validate.ResolveVersions(many)
		Expect(rv).To(HaveLen(validate.MaxReportedVersions))
		Expect(rv[0].Artifact).To(Equal("alloy-v1.18.1"), "the oldest is never the one dropped")
		Expect(versionWarnings(validate.CheckAlloyVersions(podMonitorPipeline, many))).To(
			ContainElement(ContainSubstring("11 further reported versions were not checked")))
	})

	It("checks truncated version data against the oldest shipped schema, never only the pin", func() {
		var tooNew []string
		for i := range validate.MaxReportedVersions + 10 {
			tooNew = append(tooNew, fmt.Sprintf("v9.%d.0", i))
		}
		diags := validate.CheckAlloyVersions(podMonitorPipeline, tooNew)
		Expect(versionErrors(diags)).To(ConsistOf(ContainSubstring("not available in Alloy v1.18.0")))
		Expect(versionWarnings(diags)).To(ContainElement(ContainSubstring("also checked against the oldest shipped schema, v1.18.0")))

		// Truncated by the caller (a per-collector SQL cap): the same.
		marked := validate.WithTruncatedVersions([]string{"v9.0.0"}, 5)
		diags = validate.CheckAlloyVersions(podMonitorPipeline, marked)
		Expect(versionErrors(diags)).To(ConsistOf(ContainSubstring("not available in Alloy v1.18.0")))
		Expect(versionWarnings(diags)).To(ContainElement(ContainSubstring("5 further reported versions")))
		Expect(validate.VersionSetKey(marked)).NotTo(Equal(validate.VersionSetKey([]string{"v9.0.0"})))
	})

	It("warns, naming --stability.level, when a component is experimental in the collector's version", func() {
		content := `otelcol.exporter.debug "d" {}
`
		warns := versionWarnings(validate.CheckAlloyVersions(content, []string{"v1.20.1"}))
		Expect(warns).To(ConsistOf(
			"component otelcol.exporter.debug is experimental in Alloy v1.20.1; collectors need --stability.level=experimental to load it"))
	})

	It("reports a stability warning once across versions, and a missing item once naming every version", func() {
		content := `otelcol.exporter.debug "d" {}
`
		warns := versionWarnings(validate.CheckAlloyVersions(content, []string{"v1.18.1", "v1.19.2", "v1.20.1", pinned}))
		Expect(warns).To(ConsistOf(
			"component otelcol.exporter.debug is experimental in Alloy v1.18.1, v1.19.2, v1.20.1, " + pinned +
				"; collectors need --stability.level=experimental to load it"))
		// v1.18.0 and v1.18.1 both lack the attribute: one refusal naming both.
		errs := validate.CheckAlloyVersions(podMonitorPipeline, []string{"v1.18.1", "v1.18.0"})
		Expect(versionErrors(errs)).To(HaveLen(1))
	})

	It("reads stability from the collector's own version", func() {
		content := `loki.secretfilter "s" {
  forward_to = []
}
`
		Expect(versionWarnings(validate.CheckAlloyVersions(content, []string{pinned}))).To(BeEmpty())
		warns := versionWarnings(validate.CheckAlloyVersions(content, []string{"v1.19.2"}))
		Expect(warns).To(HaveLen(1))
		Expect(warns[0]).To(ContainSubstring("public-preview in Alloy v1.19.2"))
		Expect(warns[0]).To(ContainSubstring("--stability.level=public-preview"))
	})

	It("names the schemas lacking an item on the diagnostic, for the caller to name instances", func() {
		diags := versionErrorDiags(validate.CheckAlloyVersions(redactionPipeline, []string{"v1.18.1", "v1.17.0", pinned}))
		Expect(diags).To(HaveLen(1))
		Expect(diags[0].AlloySchemas).To(ConsistOf("v1.18.0", "v1.18.1"), "v1.17.0 resolves to the oldest, v1.18.0")
	})

	It("returns nothing for content that does not parse (Stage 1 refuses it)", func() {
		Expect(validate.CheckAlloyVersions("prometheus.scrape {", []string{"v1.18.1"})).To(BeEmpty())
	})

	It("keys a version set by the schemas it resolves to and its notes", func() {
		a := validate.VersionSetKey([]string{"v1.18.1", pinned})
		b := validate.VersionSetKey([]string{pinned, "1.18.1"})
		Expect(a).To(Equal(b))
		Expect(validate.VersionSetKey([]string{pinned})).NotTo(Equal(a))
		Expect(validate.VersionSetKey([]string{"v1.20.5"})).NotTo(Equal(validate.VersionSetKey([]string{"v1.20.1"})),
			"an approximation carries a note an exact match does not")
	})
})

func versionErrorDiags(diags []validate.Diagnostic) []validate.Diagnostic {
	var out []validate.Diagnostic
	for _, d := range diags {
		if !d.Warning {
			out = append(out, d)
		}
	}
	return out
}
