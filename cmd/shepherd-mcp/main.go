// Command shepherd-mcp runs Shepherd's agent interface (W11,
// docs/gateway-tier-plan.md): a Model Context Protocol server, speaking
// stdio, that exposes a read-plus-propose slice of the shepherd.mgmt.v1
// Connect API to an AI-agent client (e.g. an editor's MCP integration).
//
// It is a separate binary, not a shepherd subcommand, because it runs as a
// distinct long-lived process under a distinct credential — one
// propose-capability service account, attributed to exactly one human (see
// internal/mcp.Config's doc comment) — rather than inside the same process
// that serves collectors and the human UI.
//
// Configuration is env-var only (no flags, no config file): this process
// has exactly four inputs and no operational knobs worth a file.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"shepherd/internal/mcp"
	"shepherd/internal/version"
)

func main() {
	if err := run(); err != nil {
		slog.Error("shepherd-mcp: fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}

	backend := mcp.NewBackend(cfg)
	server := mcp.NewServer(backend, version.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, &sdkmcp.StdioTransport{})
}

// configFromEnv reads the four required inputs. All four are required —
// there is no reduced-safety default for a missing credential or
// on-behalf-of claim, matching W10's own "reject, don't degrade" posture
// for machine attribution (docs/gateway-tier-plan.md G13).
func configFromEnv() (mcp.Config, error) {
	cfg := mcp.Config{
		BaseURL:              os.Getenv("SHEPHERD_MCP_BASE_URL"),
		ServiceAccountID:     os.Getenv("SHEPHERD_MCP_SERVICE_ACCOUNT_ID"),
		ServiceAccountSecret: os.Getenv("SHEPHERD_MCP_SERVICE_ACCOUNT_SECRET"),
		OnBehalfOf:           os.Getenv("SHEPHERD_MCP_ON_BEHALF_OF"),
	}
	var missing []string
	if cfg.BaseURL == "" {
		missing = append(missing, "SHEPHERD_MCP_BASE_URL")
	}
	if cfg.ServiceAccountID == "" {
		missing = append(missing, "SHEPHERD_MCP_SERVICE_ACCOUNT_ID")
	}
	if cfg.ServiceAccountSecret == "" {
		missing = append(missing, "SHEPHERD_MCP_SERVICE_ACCOUNT_SECRET")
	}
	if cfg.OnBehalfOf == "" {
		missing = append(missing, "SHEPHERD_MCP_ON_BEHALF_OF")
	}
	if len(missing) > 0 {
		return mcp.Config{}, fmt.Errorf("shepherd-mcp: missing required environment variable(s): %v", missing)
	}
	return cfg, nil
}
