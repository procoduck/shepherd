package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/tenancy"
)

var adminTenantPreflightCmd = &cobra.Command{
	Use:   "tenant-preflight",
	Short: "List what would stop tenants.require_owned (server-wide strict tenant mode) from starting",
	Long: `tenant-preflight runs the check Shepherd runs at start-up when
tenants.require_owned is true, without starting the server: across every
organisation it lists each live tenant route, destination and destination
binding on a tenant the organisation does not own, and each enabled pipeline
whose text sets a literal tenant (an X-Scope-OrgID header or a tenant
attribute) the organisation does not own.

It exits non-zero while anything is listed. Clear each entry by assigning the
tenant to its organisation (Admin -> Organisations), or by editing, disabling
or deleting the object, then turn the setting on.`,
	RunE: runAdminTenantPreflight,
}

func init() {
	adminCmd.AddCommand(adminTenantPreflightCmd)
}

func runAdminTenantPreflight(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()
	vs, err := tenancy.PreflightAll(cmd.Context(), st.Queries)
	if err != nil {
		return err
	}
	return printTenantPreflight(cmd.OutOrStdout(), vs)
}

// printTenantPreflight writes one line per violation and returns an error
// when there is any.
func printTenantPreflight(w io.Writer, vs []tenancy.Violation) error {
	if len(vs) == 0 {
		_, _ = fmt.Fprintln(w, "nothing uses a tenant its organisation does not own — tenants.require_owned can be turned on") //nolint:errcheck // terminal output
		return nil
	}
	for i := range vs {
		_, _ = fmt.Fprintf(w, "org %s (%s): %s\n", vs[i].OrgName, vs[i].OrgID, vs[i].Message) //nolint:errcheck // terminal output
	}
	return fmt.Errorf("%d object(s) would stop tenants.require_owned from starting; fix them first", len(vs))
}
