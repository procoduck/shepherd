package merge_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

// F-CONTRIB (#423): Assemble reports, beside the bytes it writes, which
// pipelines contributed (the matcher evidence that selected each, and the
// lines its declare block occupies) and which it left out and why — so the
// collector page reads the same decision the served config was built from,
// never a second evaluation of it.
var _ = Describe("assembly provenance (F-CONTRIB)", func() {
	reg := testRegistry()
	cl := merge.CollectorLabels{CollectorID: "coll-1", Labels: map[string]string{
		"cluster": "prod", "role": "metrics", "team": "alpha",
	}}
	ui := merge.Pipeline{
		ID: "id-ui", Name: "scrape app", Source: "ui", Revision: 4, Contents: metricsOnlyPipeline,
		Matchers: []string{`cluster="prod"`, `team=~"al.*"`, `env!="dev"`},
	}
	git := merge.Pipeline{
		ID: "id-git", Name: "from git", Source: "git", Revision: 2, Contents: metricsOnlyPipeline,
		RepoLinkCollectorID: "coll-1",
	}
	logs := merge.Pipeline{
		ID: "id-logs", Name: "logs here", Source: "wizard", Revision: 1, Contents: logsOnlyPipeline,
		Matchers: []string{`cluster="prod"`},
	}
	broken := merge.Pipeline{
		ID: "id-broken", Name: "broken", Source: "ui", Contents: metricsOnlyPipeline,
		Matchers: []string{`cluster=~"(`},
	}
	refused := merge.Pipeline{
		ID: "id-refused", Name: "refused", Source: "ui", Contents: metricsOnlyPipeline,
		Matchers: []string{`cluster="prod"`}, TenantProblem: "3:5 the tenant is not one this org owns",
	}
	elsewhere := merge.Pipeline{
		ID: "id-else", Name: "elsewhere", Source: "ui", Contents: metricsOnlyPipeline,
		Matchers: []string{`cluster="staging"`},
	}

	assemble := func(ps ...merge.Pipeline) merge.AssembleResult {
		res, err := merge.Assemble("coll-1", "prod/metrics", cl, ps, merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	// block slices content by a 1-based inclusive line span.
	block := func(content string, s merge.BlockSpan) []string {
		lines := strings.Split(content, "\n")
		Expect(s.DeclareLine).To(BeNumerically(">=", 1))
		Expect(s.EndLine).To(BeNumerically("<=", len(lines)))
		return lines[s.DeclareLine-1 : s.EndLine]
	}

	It("lists each contributor in served order with its id, revision, source and the exact lines of its block", func() {
		res := assemble(ui, git, logs, broken, refused, elsewhere)
		Expect(res.Contributors).To(HaveLen(2))
		Expect(res.Contributors[0].PipelineID).To(Equal("id-git"))
		Expect(res.Contributors[0].PipelineName).To(Equal("from git"))
		Expect(res.Contributors[0].Revision).To(Equal(2))
		Expect(res.Contributors[0].Source).To(Equal("git"))
		Expect(res.Contributors[1].PipelineID).To(Equal("id-ui"))

		for _, c := range res.Contributors {
			got := block(res.Content, c.Span)
			label := merge.DeclareLabel(c.PipelineName)
			Expect(got[0]).To(Equal(`declare "` + label + `" {`))
			Expect(got[len(got)-1]).To(Equal(label + ` "default" { }`))
			Expect(strings.Join(got, "\n")).To(ContainSubstring(`  prometheus.scrape "app" {`))
		}
		// The spans are the ones Spans recovers from the bytes.
		recovered := merge.Spans(res.Content)
		Expect(recovered).To(HaveLen(2))
		for i, c := range res.Contributors {
			Expect(c.Span.DeclareLine).To(Equal(recovered[i].DeclareLine))
			Expect(c.Span.EndLine).To(Equal(recovered[i].EndLine))
		}
	})

	It("records the matcher evidence: each matcher, the label it read and the collector's value", func() {
		res := assemble(ui)
		Expect(res.Contributors).To(HaveLen(1))
		c := res.Contributors[0]
		Expect(c.RepoLink).To(BeFalse())
		Expect(c.Matchers).To(Equal([]merge.MatcherEvidence{
			{Matcher: `cluster="prod"`, Label: "cluster", Value: "prod", Present: true},
			{Matcher: `team=~"al.*"`, Label: "team", Value: "alpha", Present: true},
			{Matcher: `env!="dev"`, Label: "env", Value: "", Present: false},
		}))
	})

	It("records that a git pipeline matched by its repo link, not by matchers", func() {
		res := assemble(git)
		Expect(res.Contributors).To(HaveLen(1))
		Expect(res.Contributors[0].RepoLink).To(BeTrue())
		Expect(res.Contributors[0].Matchers).To(BeEmpty())
	})

	It("gives every exclusion its pipeline id and a kind", func() {
		res := assemble(ui, logs, broken, refused, elsewhere)
		kinds := map[string]merge.ExclusionKind{}
		for _, ex := range res.Exclusions {
			kinds[ex.PipelineID] = ex.Kind
			Expect(ex.Reason).NotTo(BeEmpty())
		}
		Expect(kinds).To(Equal(map[string]merge.ExclusionKind{
			"id-logs":    merge.ExclusionRole,
			"id-broken":  merge.ExclusionUnparsableMatcher,
			"id-refused": merge.ExclusionTenantCheck,
		}))
	})

	It("records no contributor for an empty config and leaves the bytes as they were", func() {
		res := assemble(elsewhere)
		Expect(res.Contributors).To(BeEmpty())
		Expect(res.Content).To(ContainSubstring("// No pipelines matched\n"))
	})
})
