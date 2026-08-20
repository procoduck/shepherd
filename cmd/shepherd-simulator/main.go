// Command shepherd-simulator is the S3 sandbox-run service (VB-1 §6.4). It
// runs the pinned Alloy binary as a child process against a transformed graph
// and reports what the capture harness actually received.
//
// It is a separate binary from shepherd on purpose: it needs no database, no
// SPA and no Microsoft Graph client, and shipping it in the same image would
// put all of that inside the container whose whole job is to execute
// user-authored configuration.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"shepherd/internal/simsvc"
)

func main() {
	if err := rootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "shepherd-simulator",
		Short:         "Shepherd sandbox simulator — runs Alloy against a transformed graph and captures what it emits",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(serveCommand(), healthcheckCommand())
	// `serve` is the default so the container ENTRYPOINT needs no CMD, matching
	// how shepherd's own image is invoked.
	root.RunE = serveCommand().RunE
	return root
}

func serveCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "serve",
		Short:        "Run the simulator control API, capture harness and synthetic sources",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := simsvc.LoadConfig(os.Getenv)
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return simsvc.Serve(ctx, cfg, logger)
		},
	}
}

// healthcheckCommand exists because the final image is distroless and has no
// shell: a compose healthcheck cannot be `wget ... || exit 1`, so the binary
// probes itself. This mirrors shepherd's own healthcheck subcommand.
func healthcheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "healthcheck",
		Short:        "Probe /healthz (exits 0 if healthy, 1 otherwise)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr, err := cmd.Flags().GetString("addr")
			if err != nil {
				return fmt.Errorf("reading addr flag: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
			if err != nil {
				return fmt.Errorf("building request: %w", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("healthcheck failed: %w", err)
			}
			defer resp.Body.Close() //nolint:errcheck // process exits immediately after; close error not actionable
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
			}
			return nil
		},
	}
	cmd.Flags().String("addr", "127.0.0.1:8099", "host:port of the simulator control API")
	return cmd
}

func logLevel() slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(os.Getenv("SIM_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return level
}
