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

**The feature is disabled by default and that default is deliberate.** `simulator.enabled` has no
viper default, and both compose files default `SHEPHERD_SIM_ENABLED` to `false`. Two containment
criticals are open (B-CONTAIN-1 and B-CONTAIN-2 in the ledger): the sandbox can reach the control
plane over `sim-internal`, and `internal: true` does not deny the Docker host on every runtime.

Turning it on locally to develop against is fine. Do not enable it on a shared or production
deployment until those close — the sandbox executes user-authored Alloy config.

Without the profile, `Simulate ▾ → Sandbox run` reports that sandbox simulation is not enabled on
this server, which is the intended degradation. The other two tiers (S1 flow check, S2
relabel/log trace) need no profile and work in the default stack.

---

## Seed contents

The dev seed (`shepherd dev seed`) creates:

| Entity | Details |
|---|---|
| Orgs | `platform-org` (admin group `22222222-aaaa-4000-8000-000000000001`, reader group `…0002`) + `data-eng` (admin group `…0003`) |
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
stack uses local users only (`docker compose --profile oidc up -d` to exercise the OIDC flow).

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

| Target | Action |
|---|---|
| `make dev` | Start full dev stack (idempotent, builds images if needed) |
| `make dev-frontend` | Start Vite dev server (backend must be running) |
| `make dev-restart` | Rebuild shepherd image + restart container |
| `make dev-seed` | Re-run seed (idempotent) |
| `make dev-reset` | Stop stack + wipe all data |
| `make test-fullstack` | Run fullstack Playwright suite (boots stack, runs, tears down) |

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
