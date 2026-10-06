package cli

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/mgmtapi"
)

// #261 (maintainer decision Q1): `rerender-destinations --all` regenerates
// every wizard pipeline, which starts sending stored tenants, so it is a dry
// run unless --apply is given. The legacy run keeps its meaning.
var _ = Describe("rerender-destinations flags", func() {
	DescribeTable("decide whether the run writes",
		func(all, apply, dryRun, wantDry bool) {
			got, err := rerenderDryRun(all, apply, dryRun)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(wantDry))
		},
		Entry("legacy run writes", false, false, false, false),
		Entry("legacy --dry-run", false, false, true, true),
		Entry("--all alone is a dry run", true, false, false, true),
		Entry("--all --dry-run", true, false, true, true),
		Entry("--all --apply writes", true, true, false, false),
	)

	It("refuses --apply without --all, and --apply with --dry-run", func() {
		_, err := rerenderDryRun(false, true, false)
		Expect(err).To(MatchError(ContainSubstring("--apply only applies to --all")))
		_, err = rerenderDryRun(true, true, true)
		Expect(err).To(MatchError(ContainSubstring("contradict")))
	})

	It("prints each org's tenant destinations and says a dry run wrote nothing", func() {
		var out bytes.Buffer
		failed := printRerenderResults(&out, []mgmtapi.LegacyRerenderResult{{
			OrgID: "o1", OrgName: "acme",
			Rerendered:         []string{"self-mon"},
			TenantDestinations: []string{"mimir: acme"},
		}}, true, true)
		Expect(failed).To(Equal(0))
		Expect(out.String()).To(ContainSubstring("would re-render 1 pipeline(s)"))
		Expect(out.String()).To(ContainSubstring("tenant  mimir: acme"))
		Expect(out.String()).To(ContainSubstring("run again with --apply"))
	})
})
