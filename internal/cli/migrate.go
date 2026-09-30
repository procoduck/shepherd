package cli

import (
	"context"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/store"
)

func init() {
	c := &cobra.Command{
		Use:   "migrate",
		Short: "Apply, roll back or inspect database schema migrations (requires DB access)",
		Long: "Manages the PostgreSQL schema. `shepherd serve` does not migrate: its /readyz fails while\n" +
			"any migration is pending. The Helm chart runs `migrate up` as a pre-install/pre-upgrade Job;\n" +
			"outside the chart, run it before starting the server.",
	}
	for _, x := range []*cobra.Command{
		{Use: "up", Short: "Apply every pending migration", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateUp) }},
		{Use: "down", Short: "Roll back the most recent migration (one step)", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateDown) }},
		{Use: "status", Short: "Print the current schema version and whether it is dirty", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateStatus) }},
	} {
		c.AddCommand(x)
	}
	rootCmd.AddCommand(c)
}

func runMigration(cmd *cobra.Command, f func(context.Context, string) error) error {
	c, e := config.Load(cfgFile)
	if e != nil {
		return e
	}
	return f(cmd.Context(), c.Database.URL)
}
