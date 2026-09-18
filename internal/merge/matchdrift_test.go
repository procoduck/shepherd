package merge_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

var _ = Describe("DiffMatches", func() {
	before := merge.CollectorLabels{
		CollectorID: "coll-1",
		Labels:      map[string]string{"cluster": "prod-eu-1", "role": "metrics", "team": "platform"},
	}
	after := merge.CollectorLabels{
		CollectorID: "coll-1",
		Labels:      map[string]string{"cluster": "prod-eu-1", "role": "metrics", "team": "payments"},
	}

	It("reports 'removed' for a pipeline that matched before but not after", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "platform-only", Matchers: []string{`team="platform"`}, Source: "ui"},
		}
		diffs := merge.DiffMatches(pipelines, before, after)
		Expect(diffs).To(Equal([]merge.DiffEntry{{PipelineID: "p1", PipelineName: "platform-only", Direction: "removed"}}))
	})

	It("reports 'added' for a pipeline that didn't match before but does after", func() {
		pipelines := []merge.Pipeline{
			{ID: "p2", Name: "payments-only", Matchers: []string{`team="payments"`}, Source: "ui"},
		}
		diffs := merge.DiffMatches(pipelines, before, after)
		Expect(diffs).To(Equal([]merge.DiffEntry{{PipelineID: "p2", PipelineName: "payments-only", Direction: "added"}}))
	})

	It("omits a pipeline that matches both before and after", func() {
		pipelines := []merge.Pipeline{
			{ID: "p3", Name: "cluster-only", Matchers: []string{`cluster="prod-eu-1"`}, Source: "ui"},
		}
		Expect(merge.DiffMatches(pipelines, before, after)).To(BeEmpty())
	})

	It("omits a pipeline that matches neither before nor after", func() {
		pipelines := []merge.Pipeline{
			{ID: "p4", Name: "logs-only", Matchers: []string{`role="logs"`}, Source: "ui"},
		}
		Expect(merge.DiffMatches(pipelines, before, after)).To(BeEmpty())
	})

	It("never flips a git pipeline: it matches by collector ID, which doesn't change", func() {
		pipelines := []merge.Pipeline{
			{ID: "p5", Name: "git-pipe", Source: "git", RepoLinkCollectorID: "coll-1"},
		}
		Expect(merge.DiffMatches(pipelines, before, after)).To(BeEmpty())
	})

	It("returns one entry per flipped pipeline out of a mixed set", func() {
		pipelines := []merge.Pipeline{
			{ID: "p1", Name: "platform-only", Matchers: []string{`team="platform"`}, Source: "ui"},
			{ID: "p2", Name: "payments-only", Matchers: []string{`team="payments"`}, Source: "ui"},
			{ID: "p3", Name: "cluster-only", Matchers: []string{`cluster="prod-eu-1"`}, Source: "ui"},
		}
		diffs := merge.DiffMatches(pipelines, before, after)
		Expect(diffs).To(ConsistOf(
			merge.DiffEntry{PipelineID: "p1", PipelineName: "platform-only", Direction: "removed"},
			merge.DiffEntry{PipelineID: "p2", PipelineName: "payments-only", Direction: "added"},
		))
	})

	It("treats an unparsable matcher as no-match on that side rather than erroring", func() {
		pipelines := []merge.Pipeline{
			{ID: "p6", Name: "bad-matcher", Matchers: []string{`not a matcher`}, Source: "ui"},
		}
		Expect(func() { merge.DiffMatches(pipelines, before, after) }).NotTo(Panic())
		Expect(merge.DiffMatches(pipelines, before, after)).To(BeEmpty())
	})
})
