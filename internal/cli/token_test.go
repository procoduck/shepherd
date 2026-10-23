package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// validFixedUUID is a syntactically valid UUID for the --id flag; it never
// needs to reach the database in these cases because the dev-mode guard
// (or, for the last case, an unreachable database) is what the test proves.
const validFixedUUID = "11111111-1111-4111-8111-111111111111"

var _ = Describe("token create flag parsing", func() {
	BeforeEach(func() {
		// Reset the package-level flag vars AND the underlying pflag.Flag
		// state between cases: tokenCreateCmd is a package-level *cobra.Command
		// reused across every case, and pflag does not reset a flag's Changed
		// bit or Value on a fresh Parse — only flags actually present in the
		// next argv get touched, so a value set by one case would otherwise
		// leak into the next.
		tokenName = ""
		tokenSecret = ""
		tokenID = ""
		resetTokenFlags(tokenCreateCmd)

		GinkgoT().Setenv("SHEPHERD_DEV_ALLOW_STATIC_TOKEN", "")
		// A reachable-looking config so a case that gets past the dev-mode
		// guard fails at the database connection (fast: connection refused on
		// a port nothing listens on) rather than at config validation — that
		// distinguishes "the guard let it through" from "config.Load errored
		// first" for the last case below.
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	})

	runTokenCreate := func(args ...string) error {
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
		rootCmd.SetArgs(append([]string{"token", "create"}, args...))
		return rootCmd.Execute()
	}

	It("requires --name", func() {
		err := runTokenCreate()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("name"))
	})

	It("requires --org outside dev mode: every token is bound to an organisation", func() {
		err := runTokenCreate("--name", "x")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("--org is required"))
	})

	It("refuses a role allowlist entry no collector can register with", func() {
		err := runTokenCreate("--name", "x", "--org", "platform-org", "--roles", "bogusrole")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("bogusrole"))
	})

	It("passes the org check with --org and allowlists, and reaches the database", func() {
		err := runTokenCreate("--name", "x", "--org", "platform-org", "--clusters", "prod-eu-1,staging-eu-1", "--roles", "metrics")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("connecting to database"))
	})

	It("requires --org in dev mode too: a dev fixture is never minted unbound", func() {
		GinkgoT().Setenv("SHEPHERD_DEV_ALLOW_STATIC_TOKEN", "true")
		err := runTokenCreate("--name", "x", "--secret", "fixed-secret", "--id", validFixedUUID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("--org is required"))
	})

	It("refuses --secret outside dev mode (token.go:63)", func() {
		err := runTokenCreate("--name", "x", "--secret", "fixed-secret")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("--secret flag is only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true"))
	})

	It("refuses --id outside dev mode", func() {
		err := runTokenCreate("--name", "x", "--id", validFixedUUID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("--id flag is only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true"))
	})

	It("allows --secret and --id once SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true, and reaches the database instead of the guard", func() {
		GinkgoT().Setenv("SHEPHERD_DEV_ALLOW_STATIC_TOKEN", "true")
		err := runTokenCreate("--name", "x", "--org", "e2e-org", "--secret", "fixed-secret", "--id", validFixedUUID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("only allowed"))
		Expect(err.Error()).To(ContainSubstring("connecting to database"))
	})
})

// resetTokenFlags resets the package-level token flag vars and every flag's
// Changed bit and value on c (see the create suite's BeforeEach for why).
func resetTokenFlags(c *cobra.Command) {
	tokenOrg = ""
	tokenClusters = nil
	tokenRoles = nil
	c.Flags().VisitAll(func(f *pflag.Flag) {
		f.Changed = false
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil) //nolint:errcheck // clearing a string slice cannot fail
			return
		}
		_ = f.Value.Set(f.DefValue) //nolint:errcheck // resetting a flag to its own default cannot fail
	})
}

var _ = Describe("token bind flag parsing", func() {
	BeforeEach(func() {
		tokenID = ""
		resetTokenFlags(tokenBindCmd)
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	})

	runTokenBind := func(args ...string) error {
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
		rootCmd.SetArgs(append([]string{"token", "bind"}, args...))
		return rootCmd.Execute()
	}

	It("requires --org", func() {
		err := runTokenBind("--id", validFixedUUID)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`"org"`))
	})

	It("requires --id", func() {
		err := runTokenBind("--org", "platform-org")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`"id"`))
	})

	It("refuses an id that is not a UUID", func() {
		err := runTokenBind("--id", "not-a-uuid", "--org", "platform-org")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid --id"))
	})

	It("reaches the database with a valid id and org", func() {
		err := runTokenBind("--id", validFixedUUID, "--org", "platform-org", "--roles", "metrics")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("connecting to database"))
	})
})
