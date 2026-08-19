# Shepherd — Developer Guide

## Quick start (3 commands)

```bash
git clone <repo>
make build-web && make docker-build && make docker-build-init
make dev
# → Open http://localhost:8080 and log in as admin / admin
```

The dev stack starts in ~10s (images cached). It includes:
- Shepherd at `:8080` with the embedded SPA
- PostgreSQL at `:15432` (named volume — data persists across restarts)
- `mockmsft` at `:9090` — mock Microsoft Graph API for group search

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

Add `--profile` flags to include extra services:

```bash
# With Alloy agent (Collectors screen shows a real live collector)
docker compose -f dev/docker-compose.dev.yaml --profile alloy up -d --build --wait

# With mock-OAuth2 server (for OIDC login flow testing)
docker compose -f dev/docker-compose.dev.yaml --profile oidc up -d --build --wait
```

---

## Seed contents

The dev seed (`shepherd dev seed`) creates:

| Entity | Details |
|---|---|
| Orgs | `platform-org` (admin group: `22222222-aaaa-4000-8000-000000000001`) + `data-eng` |
| Clusters | `prod-eu-1` (claimed by platform-org), `staging-us-1` (unclaimed) |
| Collectors | `metrics` (APPLIED), `logs` (stale, 2h old), `singleton` (FAILED) — all on prod-eu-1 |
| Pipelines | `base-metrics` (ui, enabled), `loki-logs` (ui, disabled), `app-obs-wizard` (wizard) |
| Agent token | ID `00000000-dev0-4000-a000-000000000001`, secret `dev-only-agent-secret-32byteslong` |

To reseed without resetting data: `make dev-seed` (idempotent — all inserts use `ON CONFLICT DO NOTHING`).

---

## Credentials

| Service | Credentials |
|---|---|
| Local admin login | `admin` / `admin` |
| Database (direct) | `postgres://shepherd:shepherd@localhost:15432/shepherd_dev` |
| Agent token | See seed contents above |

`dev/shepherd.dev.env` is committed and holds only dev-only fixtures. OIDC is deliberately
left unset there: the `oidc` service sits behind the `oidc` compose profile, so the default
stack is local-admin only (`docker compose --profile oidc up -d` to exercise the OIDC flow).

**Change the password:** Generate a new hash and update `dev/shepherd.dev.env`:
```bash
./bin/shepherd hash-password --password-stdin <<< "my-new-password"
# Paste the output as SHEPHERD_AUTH_LOCAL_ADMIN_PASSWORD_HASH in dev/shepherd.dev.env.
# Every `$` must be doubled to `$$` — docker compose interpolates the env file.
```

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
