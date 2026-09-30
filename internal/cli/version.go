package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"shepherd/internal/version"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{Use: "version", Short: "Print the Shepherd version, commit and build date", Run: func(*cobra.Command, []string) {
		fmt.Printf("shepherd version=%s commit=%s date=%s\n", version.Version, version.Commit, version.Date)
	}})
}
