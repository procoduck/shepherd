# Shepherd — Developer Guide

## Quick start (3 commands)

```bash
git clone <repo>
make dev   # builds the shepherd images itself, then boots the stack
# → Open http://localhost:8080 and log in as admin / admin
```

The dev stack starts in ~10s (images cached). It includes:
- Shepherd at `:8080` with the embedded SPA
- PostgreSQL at `:15432` (named volume — data persists across restarts)
- `mockmsft` at `:9090` — mock Microsoft Graph API for group search
- Gitea (git server for the seeded GitOps fixtures)
- Three real Alloy agents (prod-eu-1/metrics, prod-eu-1/logs, staging-eu-1/metrics), so the
  Collectors screen shows live instances

---

## Modes

### Mode A — Full-stack (default: `make dev`)

Everything runs in Docker. The SPA is embedded in the shepherd binary.
Suitable for testing real flows, running the fullstack test suite, or sharing
a reproducible state.

**Go code changes:** Requires image rebuild.
```bash
make dev-restart   # rebuilds shepherd image (~5s cached) + restarts container
```

### Mode B — Frontend dev server (`make dev-frontend`)

Vite runs at `:5173` with HMR; backend at `:8080` in Docker.
Changes to React components are reflected immediately without rebuilding.

```bash
make dev           # start backend stack
make dev-frontend  # in another terminal: starts Vite with /api and /auth proxied to :8080
```

**Port note:** The backend sets `SameSite=Lax` cookies. Because both ports are `localhost`,
cookies are shared — no special configuration needed. `SHEPHERD_AUTH_INSECURE_COOKIES=true`
is set in the dev env file to disable the `Secure` flag for non-TLS local dev.

### Kubernetes flavour (`make dev-kind`)

```bash
make dev-kind   # ~6-10 min cold; safe to re-run on an existing cluster
```

Brings up a single-node kind cluster (`shepherd-dev`) running the real Helm chart — Calico,
Gateway API + NGINX Gateway Fabric, CloudNativePG, the same dev seed, three real Alloy agents,
Gitea, and the navikt mock OIDC provider — reachable at `http://shepherd.localtest.me`,
`http://gitea.localtest.me` and `http://oidc.localtest.me` (port 80, no `:8080`). It's the
Kubernetes counterpart to `make dev` above, not a test suite — see
`docs/kind-test-environment-plan.md` §11 for the full design (routing/DNS, the OIDC boundary,
`reload` mechanics, where the manifests live).

- **URLs and login:** `http://shepherd.localtest.me`, `admin` / `admin` — the same seed contents
  and credentials as compose (below), plus an SSO button ("Mock SSO") the compose stack only
  offers behind the `oidc` profile: here the mock issuer is declared in the chart values, so it's
  on by default.
- **Seed parity:** `make dev-kind` runs the identical `shepherd dev seed` as `make dev`
  (`kubectl exec deploy/shepherd -- /usr/local/bin/shepherd dev seed`) — same orgs, users, Gitea
  repo and agent token as the Seed contents table below.
- **The OIDC walk, briefly:** sign in with the "Mock SSO" button, enter a username and a JSON
  `groups` claim on mock-oauth2-server's login page. A group matching the seeded app-admin group
  id signs in as an app admin; a group matching a seeded org's admin/reader group id signs in with
  that org role. `/admin/auth` is **read-only** against this stack — the chart's `oidc.issuer`
  value means the SSO settings page cannot be used to add or test a different provider (a
  deliberate `internal/auth` boundary, not a dev-stack gap; §11 of the plan above has the exact
  refusal text).
- **Offline:** `*.localtest.me` needs public DNS (it resolves every subdomain to `127.0.0.1`); with
  none, add `shepherd.localtest.me`, `oidc.localtest.me` and `gitea.localtest.me` to `/etc/hosts`.
- **`make dev-kind-down` deletes the cluster and all its data** — CNPG's and Gitea's storage are
  both local-path PVs that die with it, the same as `make dev-reset` wiping compose's named
  volumes.
- **Agents do not run this.** No coding agent creates, uses or deletes the `shepherd-dev` cluster,
  or runs any `make dev-kind*` / `make e2e-k8s` target, as part of automated work — only one such
  cluster can exist and only one process can bind host port 80 on a shared machine.

---

## Optional profiles

Add `--profile` flags to include extra services (the Alloy agents and Gitea need no profile —
they start by default):

```bash
# With mock-OAuth2 server (for OIDC login flow testing)
docker compose -f dev/docker-compose.dev.yaml --profile oidc up -d --build --wait
```

### The `sim` profile — S3 sandbox simulation

`make dev-sim` builds the simulator image and brings the stack up with the sandbox tier enabled:

```bash
make dev-sim   # == SHEPHERD_SIM_ENABLED=true docker compose ... --profile sim up
```

**The posture is different in every artifact — none of them is "disabled and that's deliberate"
across the board any more; both containment gates (B-CONTAIN-1, B-CONTAIN-2) closed 2026-08-21
and F5 is CLOSED in `docs/project-status.md`:**

| Artifact | Default | Why |
|---|---|---|
| Shepherd binary (viper) | `simulator.enabled` has **no default** — off unless config sets it | Library default stays conservative; something above it (chart or an operator) opts in explicitly |
| Helm chart (`deploy/helm/shepherd`) | `simulator.enabled: true` — **on since v0.0.1** | Both containment gates closed: bind-address hardening + `P-shepherd-deny` probes (B-CONTAIN-1), and NetworkPolicy enforcement verified in a real cluster (`e2e/k8s/simulator_containment_test.go`, all seven Layer B probes + the kill probe). Ships with its default-deny NetworkPolicy, never without |
| `dev/docker-compose.dev.yaml` / `e2e/docker-compose.e2e.yaml` | opt-in `sim` profile, `SHEPHERD_SIM_ENABLED` defaults `false` | Deliberate, and unrelated to the containment gates: the default `make e2e` exercises the simulator-*absent* path on purpose; compose is a local-dev convenience, not the containment boundary |

`make dev-sim` builds the simulator image and brings the stack up with the profile and the env
var both on — this is the one path in the table above that still requires an explicit opt-in.

**B-CONTAIN-2 remains a documented local-dev-only caveat**, unrelated to the chart default above:
on OrbStack/Docker Desktop, `internal: true` does not deny the bridge gateway, so every
host-published port is reachable from the sandbox network. This is a Docker-bridge artifact with
no Kubernetes equivalent (the chart's NetworkPolicy is enforced by the CNI, not by compose), so it
stays documented rather than fixed. Turning `dev-sim` on locally to develop against is fine.

Without the profile, `Simulate ▾ → Sandbox run` reports that sandbox simulation is not enabled on
this server, which is the intended degradation. The other two tiers (S1 flow check, S2
relabel/log trace) need no profile and work in the default stack.

**Sandbox-run UI coverage exists**: `web/tests/fullstack/sandbox-run.spec.ts` self-skips unless
`FULLSTACK_SIM=1`. `make test-fullstack-sim` (local-only, not in CI — D13) brings the `sim`
profile up and runs just that spec; plain `make test-fullstack` still runs against the
sim-disabled default stack, same as `make dev` without `dev-sim`.

---

## Seed contents

The dev seed (`shepherd dev seed`) creates:

| Entity | Details |
|---|---|
| Orgs | `platform-org` (admin group `22222222-aaaa-4000-8000-000000000001`, reader group `…0002`) + `data-eng` (admin group `…0003`) |
| Local users | Bootstrap admin (§ Credentials below) plus two more on `platform-org`: `editor` / `editor-dev-pass` (`org_members.role = editor`) and `viewer` / `viewer-dev-pass` (`role = viewer`) — deterministic fixtures for exercising the org-editor/viewer tiers without an OIDC provider (`internal/cli/dev.go`'s `seedLocalUsers`) |
| Clusters | `prod-eu-1`, `staging-eu-1` (both claimed by platform-org), `data-eng-eu-1` (claimed by data-eng) |
| Collectors | `metrics`, `logs`, `singleton` on prod-eu-1; `metrics` on data-eng-eu-1. Collector rows only — instances register themselves from the compose Alloy containers; `singleton` shows zero instances until something registers, which is expected |
| Pipelines (platform-org) | `base-metrics` (ui, enabled), `demo-visual` (visual, enabled — real `alloy-graph/v1` wizard_state so the visual builder opens with an editable example), `loki-logs` (ui, disabled), `app-obs-wizard` (wizard, disabled) |
| Pipelines (data-eng) | `example-metrics` (ui, disabled) |
| Destinations | `prom-prod` (prometheus), `loki-prod` (loki) — platform-org |
| GitOps | Gitea repo `shepherd-demo-config` + `pat` credential `gitea-demo` + repo link → git-sourced `demo-git.alloy` pipeline (skipped with a notice if Gitea is unreachable) |
| Agent token | ID `00000000-de00-4000-a000-000000000001`, secret `dev-only-agent-secret-32byteslong` |

To reseed without resetting data: `make dev-seed` (idempotent — inserts use `ON CONFLICT DO NOTHING`, orgs `ON CONFLICT DO UPDATE`).

---

## Credentials

| Service | Credentials |
|---|---|
| Local admin login | `admin` / `admin` |
| Database (direct) | `postgres://shepherd:shepherd@localhost:15432/shepherd_dev` |
| Agent token | See seed contents above |

`dev/shepherd.dev.env` is committed and holds only dev-only fixtures. OIDC is deliberately
left unset there: the `oidc` service sits behind the `oidc` compose profile, so the default
compose stack uses local users only (`docker compose --profile oidc up -d` to exercise the OIDC
flow). The Kubernetes flavour (`make dev-kind`, above) does it differently: the mock issuer is
declared directly in `dev/kind/values.yaml`'s chart values, so SSO is available there with no
extra profile step — and, because it's chart-declared, the SSO settings page is read-only against
it.

**Change the password:** the first administrator is seeded on first boot from
`SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD` in `dev/shepherd.dev.env` (currently
`admin`), and only while the users table is empty. To pick a different one,
change the value and recreate the volume:

```bash
make dev-reset && make dev
```

On a running stack, change it in the UI instead — the seed is not consulted
again. No more doubling `$` for compose: the value is a plaintext password the
server hashes, not an argon2 string full of `$`.

---

## Persona sessions for testing

Use `shepherd dev create-session` to mint a DB-backed session for any persona:

```bash
# From inside the running shepherd container:
docker compose -f dev/docker-compose.dev.yaml exec shepherd \
  /usr/local/bin/shepherd dev create-session --persona orgadmin-platform

# Personas: appadmin | orgadmin-platform | reader-platform | nobody
# Output: session_id=dev-orgadmin-platform-1234567890
# Set cookie: shepherd_session=<session_id>
```

This is a direct DB insert — no HTTP flow. **Never use in production.**

---

## Makefile targets

Full list (`make help` prints the same, plus the `E2E_*` env knobs each test target honors).

**Dev stack**

| Target | Action |
|---|---|
| `make dev` | Start the local dev stack (idempotent, builds images if needed) — login `admin`/`admin` at `:8080` |
| `make dev-frontend` | Start the Vite dev server (HMR) against the running dev backend |
| `make dev-restart` | Rebuild the shepherd image + restart its container (5-10s with layer cache) |
| `make dev-seed` | Re-run the dev seed (idempotent — safe on a running stack) |
| `make dev-reset` | Stop the dev stack and wipe all data (named volumes) |
| `make dev-sim` | Start the dev stack with the S3 sandbox simulator (builds the simulator image; opt-in — see Optional profiles above) |
| `make dev-kind` | Kubernetes flavour: kind cluster `shepherd-dev` with Calico, CNPG, Gateway API + NGF, the chart, Gitea and mock OIDC (`http://shepherd.localtest.me`) — see Kubernetes flavour above |
| `make dev-kind-reload` | Rebuild `shepherd:local`, load it into `shepherd-dev` and roll the pods (migrations run first) |
| `make dev-kind-seed` | Re-run the dev seed inside `shepherd-dev` (idempotent) |
| `make dev-kind-status` | Show `shepherd-dev`'s workloads, routes, DNS rewrite and URLs |
| `make dev-kind-down` | Delete the `shepherd-dev` cluster and all its data — the `dev-reset` equivalent |

**Build**

| Target | Action |
|---|---|
| `make build` | Build the shepherd binary (embeds the SPA built by `build-web`) |
| `make build-web` | Build the React SPA into `internal/spa/dist` |
| `make build-all` | Alias of `build` |
| `make docker-build-local` | Build `shepherd:local` from `deploy/Dockerfile.local` |
| `make docker-build-init` | Build `shepherd:local-init` (init/CLI image for migrate + seed) |
| `make docker-build-simulator` | Build the S3 sandbox simulator image |
| `make release-snapshot` | GoReleaser dry run |
| `make clean` | Remove build outputs (`bin/`, goreleaser `dist/`) |
| `make clean-docker` | Remove the local `shepherd:*` / `shepherd-simulator:*` image tags |

**Test**

| Target | Action |
|---|---|
| `make test` | Run all Go tests (requires Docker) |
| `make test-cover` | Run all Go tests with a coverage profile (requires Docker) |
| `make test-ui` | Mocked Playwright suite (no backend required) |
| `make test-fullstack` | Playwright fullstack suite against the dev stack (boots it, runs, tears down) |
| `make test-fullstack-sim` | The sandbox-run fullstack spec against the dev stack's `sim` profile (local-only, not in CI) |
| `make web-ci` | Run CI's web job locally (typecheck + tests + biome check + build) |
| `make smoke` | Container smoke test (< 60s, Docker only) |
| `make e2e` | Compose e2e suite, real Alloy agent (~10 min) |
| `make e2e-sim` | S3 sandbox e2e: containment probes + run lifecycle |
| `make e2e-egress` | Sandbox egress containment probes only (fast local check) |
| `make e2e-k8s` | Kubernetes e2e suite on a fresh kind cluster (~3-5 min; 45m timeout budget) |
| `make e2e-k8s-clean` | Delete kind clusters a SIGKILLed `e2e-k8s` run left behind |
| `make schema-verify` | Verify the committed Alloy schema artifact matches the pinned version |
| `make helm-lint` | Lint + template the Helm chart against every `ci/` value file |
| `make chart-verify` | Verify the vendored chart schema matches upstream (network; occasional) |

**Lint, guards, codegen**

| Target | Action |
|---|---|
| `make lint` | Repo guards + golangci-lint |
| `make guards` | Run all ten repo-shape guards |
| `make fmt` | Format Go code |
| `make vulncheck` | Guard: no known-reachable vulnerabilities (govulncheck) |
| `make generate` | Regenerate buf + sqlc code (and the Alloy version constant) |
| `make generate-corpus` | Regenerate visual-builder goldens |
| `make schema` | Regenerate the Alloy schema artifact (network + docker; occasional) |
| `make docs` | Regenerate `site/docs/` from `scripts/docs-content/` |
| `make tools` | Install the Go-installable CLIs the targets here shell out to |

Individual guard targets (`check-single-dist`, `check-dist-consistency`, `check-build-script`,
`check-raw-sql`, `check-docker`, `check-no-route-mocks`, `check-gateway-pin`,
`check-chartvalues-pin`, `check-docs-drift`, `check-docs-version`) are `make lint`/`make guards`
prerequisites, not meant to be run standalone day to day — see the Makefile header for what each
one checks.

---

## Troubleshooting

**Port 15432 in use:** Another postgres is running on that port. Stop it or change
`15432:5432` in `dev/docker-compose.dev.yaml`.

**`shepherd_session` cookie not set:** Ensure `SHEPHERD_AUTH_INSECURE_COOKIES=true` is
in `dev/shepherd.dev.env`. Without it, the `Secure` flag prevents the cookie from being
set on non-HTTPS origins.

**Login redirects in a loop:** The server's `/api/me` returns `401` for unauthenticated
requests. The SPA redirects to `/login`. If you see a redirect loop, clear all `localhost`
cookies in the browser.

**Seed fails:** If `shepherd dev seed` fails with a connection error, the postgres
healthcheck may not have passed yet. Wait a few seconds and retry, or run `make dev-reset`
followed by `make dev`.
