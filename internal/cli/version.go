package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"shepherd/internal/version"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) {
		fmt.Printf("shepherd version=%s commit=%s date=%s\n", version.Version, version.Commit, version.Date)
	}})
}
