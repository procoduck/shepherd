package merge_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/merge"
)

var _ = Describe("strict tenant mode's serve-time exclusion (Pipeline.TenantProblem)", func() {
	reg := testRegistry()
	cl := merge.CollectorLabels{CollectorID: "coll-uuid-1", Labels: map[string]string{"cluster": "test", "role": "metrics"}}
	pipes := func(problem string) []merge.Pipeline {
		return []merge.Pipeline{
			{Name: "clean", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui", Revision: 2},
			{Name: "refused", Contents: metricsOnlyPipeline, Matchers: []string{`cluster="test"`}, Source: "ui", Revision: 3, TenantProblem: problem},
		}
	}

	It("leaves the refused pipeline out and lists it in its own header section", func() {
		r, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipes("3:5 the labels are not a literal map"), merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("pipe_clean"))
		Expect(r.Content).NotTo(ContainSubstring("pipe_refused"))
		Expect(r.Content).To(ContainSubstring("// Excluded (1) - strict tenant check:\n//   - refused: 3:5 the labels are not a literal map\n"))
		Expect(r.Content).NotTo(ContainSubstring("signal/role mismatch"))
		Expect(r.Exclusions).To(ConsistOf(merge.Exclusion{
			PipelineName: "refused", Reason: "3:5 the labels are not a literal map", Kind: merge.ExclusionTenantCheck,
		}))
	})

	It("writes no strict section when nothing is refused, and changes nothing for a collector it does not match", func() {
		with, err := merge.Assemble("coll-uuid-1", "test/metrics", cl, pipes(""), merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(with.Content).NotTo(ContainSubstring("strict tenant check"))
		Expect(with.Content).To(ContainSubstring("pipe_refused"))
		other := merge.CollectorLabels{CollectorID: "coll-uuid-2", Labels: map[string]string{"cluster": "other", "role": "metrics"}}
		a, err := merge.Assemble("coll-uuid-2", "other/metrics", other, pipes(""), merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		b, err := merge.Assemble("coll-uuid-2", "other/metrics", other, pipes("refused"), merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(b.Content).To(Equal(a.Content))
	})

	It("keeps the role section and the strict section apart", func() {
		logs := merge.CollectorLabels{CollectorID: "coll-uuid-1", Labels: map[string]string{"cluster": "test", "role": "logs"}}
		r, err := merge.Assemble("coll-uuid-1", "test/logs", logs, pipes("refused"), merge.WithRoleEnforcement(reg))
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Content).To(ContainSubstring("// Excluded (1) - signal/role mismatch"))
		Expect(r.Content).To(ContainSubstring("// Excluded (1) - strict tenant check:\n//   - refused: refused\n"))
	})
})
