package cli

import (
	"context"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/store"
)

func init() {
	c := &cobra.Command{Use: "migrate"}
	for _, x := range []*cobra.Command{{Use: "up", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateUp) }}, {Use: "down", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateDown) }}, {Use: "status", RunE: func(cmd *cobra.Command, _ []string) error { return runMigration(cmd, store.MigrateStatus) }}} {
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
