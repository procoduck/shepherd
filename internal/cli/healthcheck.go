package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:          "healthcheck",
		Short:        "Check /healthz endpoint (exits 0 if healthy, 1 otherwise)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr, err := cmd.Flags().GetString("addr")
			if err != nil {
				return fmt.Errorf("reading addr flag: %w", err)
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
			if err != nil {
				return fmt.Errorf("building request: %w", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("healthcheck failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
			}
			return nil
		},
	}
	cmd.Flags().String("addr", "localhost:8080", "host:port of the shepherd server")
	rootCmd.AddCommand(cmd)
}
