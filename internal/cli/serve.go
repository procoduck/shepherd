package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/server"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Run the Shepherd server: web UI, management API and the collector (remotecfg) API",
		RunE:  runServe,
	})
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (valid values: debug, info, warn, error)", s)
	}
}

func buildLogHandler(format string, w io.Writer, lvl slog.Level) (slog.Handler, error) {
	opts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	case "text":
		return slog.NewTextHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("invalid log format %q (valid values: json, text)", format)
	}
}

// newServeLogger builds the server's logger and installs it as the slog
// default. The server passes it to every component it constructs, but a few
// packages log through the package-level slog functions (internal/tenancy's
// tenant-index warnings, internal/validate's version check, among others);
// without the default they wrote plain text into a JSON log stream (0.21
// walkthrough).
func newServeLogger(format string, w io.Writer, lvl slog.Level) (*slog.Logger, error) {
	h, err := buildLogHandler(format, w, lvl)
	if err != nil {
		return nil, err
	}
	l := slog.New(h)
	slog.SetDefault(l)
	return l, nil
}

func runServe(*cobra.Command, []string) error {
	c, e := config.Load(cfgFile)
	if e != nil {
		return fmt.Errorf("loading config: %w", e)
	}
	if level, err := rootCmd.PersistentFlags().GetString("log-level"); err == nil && level != "" {
		c.Log.Level = level
	}
	if format, err := rootCmd.PersistentFlags().GetString("log-format"); err == nil && format != "" {
		c.Log.Format = format
	}
	lvl, err := parseLogLevel(c.Log.Level)
	if err != nil {
		return err
	}
	l, err := newServeLogger(c.Log.Format, os.Stderr, lvl)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, e := server.New(c, l)
	if e != nil {
		return e
	}
	return s.Run(ctx)
}
