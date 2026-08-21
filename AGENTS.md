# Shepherd

Self-hosted Grafana Alloy fleet manager. Go 1.26 backend (Connect RPC agent API + chi REST),
React 18/TS/Vite SPA embedded via go:embed, PostgreSQL 16. Spec: docs/spec.md (authoritative).

## Commands
- Discover targets: `make help` (lists all targets + env knobs E2E_KEEP/E2E_K8S_KEEP/E2E_K8S_NODE_IMAGE)
- Build: `make build` (builds web first — required for go:embed)
- Test all: `make test` (all Go tests; needs Docker for testcontainers) · single pkg: `ginkgo ./internal/<pkg>` · focused: `ginkgo --focus "name" ./internal/<pkg>`
- E2E (needs Docker, ~10 min): `make e2e` · sandbox: `make e2e-sim` / `make e2e-egress` · Kubernetes (kind): `make e2e-k8s`
- Mocked UI suite: `make test-ui` · fullstack Playwright (needs Docker dev stack): `make test-fullstack`
- Local dev stack: `make dev` (boots at :8080, login admin/admin) · `make dev-reset` (wipe data)
- Lint+format: `make lint` / `make fmt` (golangci-lint v2) · Helm chart: `make helm-lint`
- Codegen after proto/SQL changes: `make generate`
- Tool bootstrap: `make tools` (ginkgo, gofumpt, sqlc, buf) · cleanup: `make clean` / `make clean-docker`
- Schema artifact drift check: `make schema-verify` · container smoke test: `make smoke`
- Release dry-run: `make release-snapshot`

## Architecture
- `cmd/shepherd/` — cobra entrypoint; `internal/cli/` — subcommands
- `internal/agentapi/` — collector.v1 Connect service (the protocol Alloy polls)
- `internal/mgmtapi/` — REST handlers · `internal/auth/` — OIDC BFF + RBAC middleware
- `internal/merge/` — matcher eval + declare-wrap merge + hashing · `internal/validate/` — 3-stage gate
- `internal/store/` — sqlc output + repositories · `internal/migrations/sql/` — golang-migrate SQL
- `internal/graph/`, `internal/ado/`, `internal/gitsync/`, `internal/gitrepo/` — Entra Graph, ADO auth, repo sync, git transport
- `internal/visual/`, `internal/schema/` — graph→Alloy renderer, component schema artifact + overlay
- `internal/simulate/`, `internal/simsvc/`, `internal/netshape/` — S2/S3 simulation transform, sandbox simulator service, host-literal analysis
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

| Upstream image | Pin lives in |
|---|---|
| `gcr.io/distroless/static-debian12:nonroot` | `deploy/versions.env` (DISTROLESS_IMAGE) |
| `gcr.io/distroless/base-debian12:nonroot` | `deploy/versions.env` (DISTROLESS_BASE_IMAGE) |
| `grafana/alloy:v1.18.1` | `deploy/versions.env` (ALLOY_IMAGE) |
| `golang:1.26-alpine` | `deploy/versions.env` (GO_IMAGE) |
| `node:24-slim` | `deploy/versions.env` (NODE_IMAGE) |
| `postgres:16-alpine` | compose files, `Makefile` (smoke), `internal/testutil/postgres.go` — NOT versions.env |
| `ghcr.io/navikt/mock-oauth2-server:6.0.1` | compose files — NOT versions.env |
| `gitea/gitea:1-rootless` | compose files (e2e + dev) — NOT versions.env |
| `alpine:3.22` | `e2e/docker-compose.e2e.yaml` (probe helper) — NOT versions.env |

`deploy/versions.env` is the source of truth for the rows that name it; the rest are
hardcoded where the table says. Update pins there first.

This applies to: `deploy/Dockerfile.local`, `deploy/Dockerfile.goreleaser`, `e2e/docker-compose.e2e.yaml`,
and any `testcontainers-go` image string (e.g. in `internal/testutil/postgres.go`).
