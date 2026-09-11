# Shepherd

Self-hosted Grafana Alloy fleet manager. Go 1.26 backend (Connect RPC agent API + chi REST),
React 19/TS/Vite SPA embedded via go:embed, PostgreSQL 16. Spec: docs/spec.md (authoritative).

## Commands
- Discover targets: `make help` (lists all targets + env knobs E2E_KEEP/E2E_K8S_KEEP/E2E_K8S_NODE_IMAGE)
- Build: `make build` (builds web first — required for go:embed)
- Test all: `make test` (all Go tests; needs Docker for testcontainers) · single pkg: `ginkgo ./internal/<pkg>` · focused: `ginkgo --focus "name" ./internal/<pkg>`
- E2E (needs Docker, ~10 min): `make e2e` · sandbox: `make e2e-sim` / `make e2e-egress` · Kubernetes (kind): `make e2e-k8s`
- CI cadence (D11): `e2e` (agent-protocol suite) runs on push to main path-filtered to what it exercises, plus `merge_group` and manual dispatch — not on every PR; the `e2e-egress` containment job additionally runs on any PR touching the sandbox surface (`internal/simsvc`, `internal/simulate`, `internal/netshape`, `cmd/shepherd-simulator`, `deploy/Dockerfile.simulator`). `e2e-k8s` is its own workflow, path-filtered to `deploy/helm/**`, `e2e/k8s/**`, itself, `deploy/Dockerfile*`, `deploy/versions.env`.
- Mocked UI suite: `make test-ui` · fullstack Playwright (needs Docker dev stack): `make test-fullstack`
- Reproduce CI's web job exactly (typecheck + tests + biome CHECK + build): `make web-ci` — `pnpm lint` alone (== `check:ci`, Biome's read-only check — it does catch formatting now) skips typecheck, tests and the build, so a type error or a failing test can pass `pnpm lint` and still fail CI
- Local dev stack: `make dev` (boots at :8080, login admin/admin) · `make dev-reset` (wipe data) · `make dev-sim` (adds the sandbox simulator) · `make dev-frontend` / `make dev-restart` · `make dev-seed` (dev-only: also creates local editor/viewer users)
- Lint+format: `make lint` / `make fmt` (golangci-lint v2 + the ten repo-shape guards, see `make guards`) · Helm chart: `make helm-lint` / `make chart-verify`
- Codegen after proto/SQL changes: `make generate` · visual-builder test corpus: `make generate-corpus`
- Tool bootstrap: `make tools` (ginkgo, sqlc, buf, govulncheck) · cleanup: `make clean` / `make clean-docker`
- Schema artifact drift check: `make schema-verify` · container smoke test: `make smoke` · supply-chain scan: `make vulncheck` · coverage: `make test-cover`
- Docs site (generated, do not hand-edit `site/docs/`): edit `scripts/docs-content/`, then `make docs` to regenerate — `make check-docs-drift` (part of `make lint`) fails if the committed `site/docs/` disagrees with the generator, `make check-docs-version` fails if the docs' quoted chart/app version disagrees with `deploy/helm/shepherd/Chart.yaml`
- Release dry-run: `make release-snapshot`

## Architecture
- `cmd/shepherd/` — cobra entrypoint; `internal/cli/` — subcommands
- `internal/agentapi/` — collector.v1 Connect service (the protocol Alloy polls)
- `internal/mgmtapi/` — `shepherd.mgmt.v1` Connect services (+ a legacy REST shim) · `internal/auth/` — OIDC BFF + RBAC middleware
- `internal/merge/` — matcher eval + declare-wrap merge + hashing · `internal/validate/` — 3-stage gate
- `internal/serve/` — `ComputeServed`, the single merge→validate→append-baseline recompute path shared by `agentapi` and `mgmtapi`
- `internal/store/` — sqlc output + repositories · `internal/migrations/sql/` — golang-migrate SQL
- `internal/graph/`, `internal/ado/`, `internal/gitsync/`, `internal/gitrepo/` — Entra Graph, ADO auth, repo sync, git transport
- `internal/visual/`, `internal/schema/` — graph→Alloy renderer, component schema artifact + overlay
- `internal/simulate/`, `internal/simsvc/`, `internal/netshape/` — S2/S3 simulation transform (DB-free), sandbox simulator service, host-literal analysis; `cmd/shepherd-simulator` is the simulator binary
- `internal/signals/` — derives a pipeline's signal set from Alloy syntax + the schema; holds the role→allowed-signals policy `internal/merge` enforces
- `internal/gateway/` — Gateway API contract (version/channel), HTTPRoute rendering, route segments, tenant-id rule, in-cluster apply with attachment verification
- `internal/receiver/` — receiver-tier Alloy pipelines (OTLP/Faro), including D10 pass-through tenancy
- `internal/beacon/` — remote_write ingest projection, baseline pipeline, inventory (D6)
- `internal/reconcile/` — declared vs served vs observed collector state
- `internal/onboarding/` — "connect an app" artifacts (env, Lambda, Terraform, SAM, CDK, k8s, SDK notes)
- `internal/chartvalues/` — k8s-monitoring Helm values layering file
- `internal/grafana/` — optional outcome verification ("did the data arrive")
- `internal/wizard/` — the wizard registry and catalog; each wizard package self-registers in `init()`
- `internal/mcp/` — MCP agent interface, read + propose only (`cmd/shepherd-mcp`)
- `internal/config/` — server configuration schema + loader · `internal/crypto/` — AES-256-GCM encryption for secrets at rest
- `internal/server/` — assembles the HTTP server with all routes · `internal/spa/` — embeds and serves the compiled React SPA (`go:embed`)
- `internal/telemetry/` — cross-cutting instrumentation (Connect interceptors, HTTP middleware, tracing) · `internal/metrics/` — Prometheus metrics for Shepherd itself
- `internal/version/` — build-time version constants · `internal/testutil/` — shared test helpers, incl. the testcontainers Postgres harness
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
- **If a bug or failing test takes more than 3 rounds of attempts to fix, stop and get an independent adversarial review before continuing** — a fresh reviewer with no stake in the current theory. Give it the exact symptom, the code under investigation, and everything already tried. Act on its findings before making further changes.
- **Verify a control at the layer it is consumed at, not only where it is written.** This repo has repeatedly shipped controls that passed their own tests and did nothing in the product: enforcement wired on one of two serve paths, a binary present in an image but unrunnable, wizard packages nothing imported. If a change alters what a *deployed* artifact does, exercise the deployed artifact.
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
| `gcr.io/distroless/base-nossl-debian12:nonroot` | `deploy/versions.env` (DISTROLESS_BASE_IMAGE) — app, init and simulator images (`make check-docker` guards `deploy/Dockerfile.*`); `e2e/mockmsft/Dockerfile:6` hardcodes `static-debian12:nonroot` and is NOT guarded |
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
