package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/pflag"
)

// M6: the CLI path into agent_identities applies the same role rule as
// AdminService.CreateAgentIdentity, before it ever opens the database.
var _ = Describe("agent-identity create role validation", func() {
	BeforeEach(func() {
		aiIssuer, aiAppID, aiOrg = "", "", ""
		aiClusters, aiRoles = nil, nil
		agentIdentityCreateCmd.Flags().VisitAll(func(f *pflag.Flag) {
			f.Changed = false
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil) //nolint:errcheck // clearing a string slice cannot fail
				return
			}
			_ = f.Value.Set(f.DefValue) //nolint:errcheck // resetting a flag to its own default cannot fail
		})
		// Unreachable database: a case that gets past validation fails at
		// the connection instead, which tells the two apart.
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	})

	run := func(args ...string) error {
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
		rootCmd.SetArgs(append([]string{"agent-identity", "create",
			"--issuer", "https://idp.example/", "--app-id", "c", "--org", "prod-org"}, args...))
		return rootCmd.Execute()
	}

	It("refuses a role that is not a collector role", func() {
		err := run("--role", "metrics", "--role", "bogusrole")
		Expect(err).To(MatchError(
			`role "bogusrole" is not a collector role: use logs, metrics, receiver or singleton`))
	})

	It("lets every collector role through to the database", func() {
		err := run("--role", "metrics,logs,receiver,singleton")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("connecting to database"))
	})
})
