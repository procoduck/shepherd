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

	// 0.21.0-rc.1 walkthrough: a failing check printed cobra's Usage block
	// under the list, as if the command had been called wrongly.
	It("fails without printing the usage block, still non-zero", func() {
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		var out bytes.Buffer
		prevUsage, prevErrors := rootCmd.SilenceUsage, rootCmd.SilenceErrors
		DeferCleanup(func() {
			rootCmd.SilenceUsage, rootCmd.SilenceErrors = prevUsage, prevErrors
			rootCmd.SetOut(nil)
			rootCmd.SetErr(nil)
			rootCmd.SetArgs(nil)
		})
		// Other specs silence the root command; this one must not rely on it.
		rootCmd.SilenceUsage, rootCmd.SilenceErrors = false, true
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"admin", "tenant-preflight"})
		Expect(rootCmd.Execute()).To(HaveOccurred())
		Expect(out.String()).NotTo(ContainSubstring("Usage:"))
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
