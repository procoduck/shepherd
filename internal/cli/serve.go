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

func init() { rootCmd.AddCommand(&cobra.Command{Use: "serve", RunE: runServe}) }
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
	h, err := buildLogHandler(c.Log.Format, os.Stderr, lvl)
	if err != nil {
		return err
	}
	l := slog.New(h)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, e := server.New(c, l)
	if e != nil {
		return e
	}
	return s.Run(ctx)
}
