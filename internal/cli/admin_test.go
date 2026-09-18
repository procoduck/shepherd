package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

// LABEL-MATCHING-PLAN.md §8's operator table: a "=" or "=~" matcher only
// ever WIDENS once admin labels are wired in (it matches nothing today,
// since an unwired key reads as ""), while a "!=" or "!~" matcher already
// matches everyone today and can only ever SHRINK. auditMatcherImpact must
// report both directions correctly for all four operators -- a tool that
// only checked "matched count went up" would miss every shrink case, which
// is the more dangerous one (a regression on something already live).
var _ = Describe("auditMatcherImpact", func() {
	collector := func(id, cluster, role string, labels map[string]string) auditCollector {
		return auditCollector{ID: id, Cluster: cluster, Role: role, AdminLabels: labels}
	}

	It("reports a widen (added) for an '=' matcher once the admin label is set", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "equals-widen", Matchers: []string{`team="platform"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"team": "platform"}),
			collector("c2", "prod", "metrics", map[string]string{"team": "payments"}),
		}
		impacts := auditMatcherImpact(pipelines, collectors)
		Expect(impacts).To(HaveLen(1))
		Expect(impacts[0].PipelineID).To(Equal("p1"))
		Expect(impacts[0].Added).To(ConsistOf(collectorRef{ID: "c1", Cluster: "prod", Role: "metrics"}))
		Expect(impacts[0].Removed).To(BeEmpty())
	})

	It("reports a widen (added) for an '=~' matcher once the admin label is set", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "regex-widen", Matchers: []string{`team=~"platform.*"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"team": "platform-eu"}),
			collector("c2", "prod", "metrics", map[string]string{"team": "payments"}),
		}
		impacts := auditMatcherImpact(pipelines, collectors)
		Expect(impacts).To(HaveLen(1))
		Expect(impacts[0].Added).To(ConsistOf(collectorRef{ID: "c1", Cluster: "prod", Role: "metrics"}))
		Expect(impacts[0].Removed).To(BeEmpty())
	})

	It("reports a shrink (removed) for a '!=' matcher when the admin label equals the excluded value", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "not-equals-shrink", Matchers: []string{`team!="platform"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			// Today: unwired "team" reads as "", "" != "platform" is true, so
			// both collectors already match every pipeline using this matcher.
			collector("c1", "prod", "metrics", map[string]string{"team": "platform"}), // becomes excluded
			collector("c2", "prod", "metrics", map[string]string{"team": "payments"}), // stays matched
		}
		impacts := auditMatcherImpact(pipelines, collectors)
		Expect(impacts).To(HaveLen(1))
		Expect(impacts[0].Removed).To(ConsistOf(collectorRef{ID: "c1", Cluster: "prod", Role: "metrics"}))
		Expect(impacts[0].Added).To(BeEmpty())
	})

	It("reports a shrink (removed) for a '!~' matcher when the admin label matches the excluded regex", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "not-regex-shrink", Matchers: []string{`team!~"platform.*"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"team": "platform-eu"}), // becomes excluded
			collector("c2", "prod", "metrics", map[string]string{"team": "payments"}),    // stays matched
		}
		impacts := auditMatcherImpact(pipelines, collectors)
		Expect(impacts).To(HaveLen(1))
		Expect(impacts[0].Removed).To(ConsistOf(collectorRef{ID: "c1", Cluster: "prod", Role: "metrics"}))
		Expect(impacts[0].Added).To(BeEmpty())
	})

	It("omits a pipeline with zero impact (e.g. a cluster/role-only matcher, unaffected by admin labels)", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "cluster-only", Matchers: []string{`cluster="prod"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"team": "platform"}),
		}
		Expect(auditMatcherImpact(pipelines, collectors)).To(BeEmpty())
	})

	It("never reports a git pipeline: it matches by collector ID, which admin labels can't change", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "git-pipe", Source: "git", RepoLinkCollectorID: "c1"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"anything": "goes"}),
		}
		Expect(auditMatcherImpact(pipelines, collectors)).To(BeEmpty())
	})

	It("aggregates multiple flipped collectors onto the same pipeline's impact entry", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "multi", Matchers: []string{`team="platform"`}, Source: "ui"},
		}
		collectors := []auditCollector{
			collector("c1", "prod", "metrics", map[string]string{"team": "platform"}),
			collector("c2", "prod", "logs", map[string]string{"team": "platform"}),
			collector("c3", "prod", "metrics", map[string]string{"team": "payments"}),
		}
		impacts := auditMatcherImpact(pipelines, collectors)
		Expect(impacts).To(HaveLen(1))
		Expect(impacts[0].Added).To(ConsistOf(
			collectorRef{ID: "c1", Cluster: "prod", Role: "metrics"},
			collectorRef{ID: "c2", Cluster: "prod", Role: "logs"},
		))
	})

	It("returns no impacts across an empty pipeline or collector set", func() {
		Expect(auditMatcherImpact(nil, []auditCollector{collector("c1", "prod", "metrics", nil)})).To(BeEmpty())
		Expect(auditMatcherImpact([]merge.Pipeline{{ID: "p1", Name: "x", Matchers: []string{`team="platform"`}, Source: "ui"}}, nil)).To(BeEmpty())
	})
})

var _ = Describe("formatCollectorRefs", func() {
	It("renders '(none)' for an empty slice", func() {
		Expect(formatCollectorRefs(nil)).To(Equal("(none)"))
	})

	It("renders comma-separated cluster/role (id) entries", func() {
		got := formatCollectorRefs([]collectorRef{
			{ID: "c1", Cluster: "prod", Role: "metrics"},
			{ID: "c2", Cluster: "prod", Role: "logs"},
		})
		Expect(got).To(Equal("prod/metrics (c1), prod/logs (c2)"))
	})
})
