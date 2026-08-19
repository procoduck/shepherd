# Shepherd

Self-hosted Grafana Alloy fleet manager. Go 1.24 backend (Connect RPC agent API + chi REST),
React 18/TS/Vite SPA embedded via go:embed, PostgreSQL 16. Spec: docs/spec.md (authoritative).

## Commands
- Build: `make build` (builds web first — required for go:embed)
- Test all: `make test` · single pkg: `ginkgo ./internal/<pkg>` · focused: `ginkgo --focus "name" ./internal/<pkg>`
- Integration (needs Docker): `make test-integration`
- E2E (needs Docker, ~10 min): `make e2e`
- Fullstack Playwright (needs Docker dev stack): `make test-fullstack`
- Local dev stack: `make dev` (boots at :8080, login admin/e2e-local-admin-pass) · `make dev-reset` (wipe data)
- Lint+format: `make lint` / `make fmt` (golangci-lint v2)
- Codegen after proto/SQL changes: `buf generate` / `sqlc generate`
- Release dry-run: `make release-snapshot`

## Architecture
- `cmd/shepherd/` — cobra entrypoint; `internal/cli/` — subcommands
- `internal/agentapi/` — collector.v1 Connect service (the protocol Alloy polls)
- `internal/mgmtapi/` — REST handlers · `internal/auth/` — OIDC BFF + RBAC middleware
- `internal/merge/` — matcher eval + declare-wrap merge + hashing · `internal/validate/` — 3-stage gate
- `internal/store/` — sqlc output + repositories · `migrations/` — golang-migrate SQL
- `internal/graph/`, `internal/ado/`, `internal/gitsync/` — Entra Graph, Azure DevOps, repo sync
- `web/` — SPA (own AGENTS.md) · `e2e/` — compose-based e2e (own AGENTS.md)

## Conventions
- Errors wrap with `fmt.Errorf("context: %w", err)`; log via slog only
- Tests are Ginkgo v2 + Gomega — no bare `testing.T` test funcs
- DB access only via sqlc queries in `internal/store`; new queries → `.sql` file + `sqlc generate`
- Enum-like fields (role, status, source) use exhaustive switches — the linter enforces it

## Rules
### Always do
- Run `make lint` and `make test` on changed packages before finishing a task
- Route every config write through `internal/validate` (3-stage gate) — no direct serve-cache writes
- Add a migration for any schema change; never edit committed migrations
- **If a bug or failing test takes more than 3 rounds of attempts to fix, stop and invoke the `adversarial-reviewer` subagent before continuing.** Describe the exact symptom, the code under investigation, and what you have already tried. Act on its findings before making further changes.
### Ask first
- New Go or npm dependencies · changes to `proto/` · changes to RBAC semantics in `internal/auth`
- Any change to the served-config content format or hashing (breaks fleet rollout)
### Never do
- Commit secrets or `.env` · log token secrets, client secrets, or session IDs
- Edit generated code (`gen/`, `internal/store/sqlc/`) — regenerate instead
- Weaken the validation gate or serve unvalidated config, even in tests of other features

## Docker image registry
All Docker images used in Dockerfiles, Compose files, and testcontainers calls
should use the standard public registries unless your organisation operates a mirror.
Replace the examples below with your internal registry prefix if needed.

| Upstream image | Reference used in this repo |
|---|---|
| `gcr.io/distroless/static-debian12:nonroot` | `gcr.io/distroless/static-debian12:nonroot` |
| `grafana/alloy:v1.18.1` | `grafana/alloy:v1.18.1` |
| `golang:1.26-alpine` | `golang:1.26-alpine` |
| `node:24-slim` | `node:24-slim` |
| `postgres:16-alpine` | `postgres:16-alpine` |
| `ghcr.io/navikt/mock-oauth2-server:6.0.1` | `ghcr.io/navikt/mock-oauth2-server:6.0.1` |

Pins live in `deploy/versions.env`; update there first, this table is kept in sync with it.

This applies to: `deploy/Dockerfile`, `deploy/Dockerfile.goreleaser`, `e2e/docker-compose.e2e.yaml`,
and any `testcontainers-go` image string (e.g. in `internal/testutil/postgres.go`).
