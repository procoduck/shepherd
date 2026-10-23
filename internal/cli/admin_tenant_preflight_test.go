package cli

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/tenancy"
)

var _ = Describe("tenant-preflight output", func() {
	It("says the setting can be turned on when nothing is listed", func() {
		var buf bytes.Buffer
		Expect(printTenantPreflight(&buf, nil)).To(Succeed())
		Expect(buf.String()).To(ContainSubstring("can be turned on"))
	})

	It("lists each violation and exits non-zero", func() {
		var buf bytes.Buffer
		err := printTenantPreflight(&buf, []tenancy.Violation{
			{OrgID: "o1", OrgName: "acme", Kind: "destination", Name: "d", Tenant: "x", Message: `destination "d" uses tenant "x"`},
		})
		Expect(err).To(MatchError(ContainSubstring("1 object(s)")))
		Expect(buf.String()).To(Equal("org acme (o1): destination \"d\" uses tenant \"x\"\n"))
	})

	It("is registered under shepherd admin", func() {
		found := false
		for _, c := range adminCmd.Commands() {
			if c.Name() == "tenant-preflight" {
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})
})
