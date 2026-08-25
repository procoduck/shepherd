# Shepherd

[![CI](https://github.com/procoduck/shepherd/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/procoduck/shepherd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/procoduck/shepherd?sort=semver)](https://github.com/procoduck/shepherd/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/procoduck/shepherd)](go.mod)
[![Licence](https://img.shields.io/github/license/procoduck/shepherd)](LICENSE)

**[procoduck.github.io/shepherd](https://procoduck.github.io/shepherd/)** — project site and quick start.

Self-hosted **Grafana Alloy fleet manager**. Shepherd serves centralised pipeline configurations to Alloy instances via the `remotecfg` protocol, providing a UI for managing pipelines, destinations, wizards, and GitOps sync from any git server (ADO and GitHub-App auth supported).

It also ships a **beacon**: every claimed collector is served a small baseline pipeline that reports which components it is running and whether they are healthy — component names only, never config text or raw samples. On by default; set `server.beacon_disabled: true` to turn it off.

**Being built, not yet reachable.** A tenant-aware gateway and receiver tier (OTLP ingest with gateway-injected tenancy), three-way reconciliation, onboarding artifacts, a k8s-monitoring chart-values generator, and a read-plus-propose MCP interface for AI agents are implemented and tested but are **not wired to a running surface**, and are gated on review sign-off. `docs/gateway-tier-plan.md` §9 tracks each one and what still stands between it and being usable. Do not plan against them yet.

---

![The visual pipeline builder: component palette, canvas, live validity and the generated Alloy config](site/img/visual-builder.jpg)

## Requirements

**To run Shepherd** you need PostgreSQL 14+ (16 is what CI and the dev stack use)
and somewhere to run one container. That is the whole list — Shepherd is a single
Go binary with the UI embedded in it, and it holds no state outside Postgres.

For Kubernetes, the chart targets a current cluster and needs no CRDs by default.
Two optional features add requirements: `metrics.serviceMonitor.enabled` needs the
Prometheus Operator's CRDs, and the (not-yet-wired) gateway tier targets Gateway
API v1.4.1, standard channel.

**Collectors** run [Grafana Alloy](https://grafana.com/docs/alloy/) v1.18.1 — the
version whose component schema this build validates against, pinned in
`deploy/versions.env`. Alloy needs `remotecfg` support, which is anything
reasonably current.

**To build it** you need Go (version per `go.mod`), Node 24 with pnpm, Docker
(the test suite starts real Postgres via testcontainers), and Helm for the chart
tests. `make tools` installs the pinned code generators.

## Quick start (local dev)

```bash
# 1. Start Postgres
docker run -d --name shepherd-pg \
  -e POSTGRES_DB=shepherd -e POSTGRES_USER=shepherd -e POSTGRES_PASSWORD=shepherd \
  -p 5432:5432 postgres:16-alpine

# 2. Build
make build

# 3. Configure. Export, don't inline: a `VAR=x cmd1 && cmd2` prefix applies to
#    cmd1 only, so `serve` would start with no config and exit.
#    KEEP THIS KEY. It encrypts git credentials and the OIDC client secret at
#    rest; a new one on the next boot cannot decrypt what the old one wrote.
export SHEPHERD_DATABASE_URL=postgres://shepherd:shepherd@localhost:5432/shepherd
export SHEPHERD_SECURITY_ENCRYPTION_KEY=$(openssl rand -base64 32)
#    The first administrator is created on first boot when the users table is
#    empty. Set a password here and it is yours; leave it unset and the account
#    is created as admin/admin and refuses to do anything until you change it.
export SHEPHERD_BOOTSTRAP_ADMIN_LOGIN=admin
export SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD=choose-a-password

# 4. Run
./bin/shepherd migrate up
./bin/shepherd serve          # http://localhost:8080, sign in as `admin`

# 5. Create an agent token (needs the same two required vars as `serve`)
./bin/shepherd token create --name dev
```

Prefer not to build it yourself? `make dev` brings up the whole stack in Docker —
Postgres, a seeded database, and Shepherd at `http://localhost:8080` with
`admin` / `admin`.

## Alloy spoke config

Add to your Alloy `config.alloy`:

```alloy
remotecfg {
  url = "http://shepherd:8080"
  basic_auth {
    username = "<token-uuid>"
    password = "<token-secret>"
  }
  attributes = {
    cluster = "<cluster-name>",
    role    = "metrics",
  }
  poll_frequency = "60s"
}
```

---

## Management API for integrators

The `/api/*` REST surface (`/api/orgs/{org}/pipelines`, `/api/admin/orgs`, `/api/orgs/{org}/destinations`,
etc.) is unchanged for existing integrations — same paths, same JSON shapes, same session-cookie +
CSRF auth. Under the hood those routes are now thin shims over a typed Connect RPC contract
(`shepherd.mgmt.v1`; see `docs/archive/api-contract-design.md`). The Connect endpoints are plain HTTP
POST + JSON themselves, so integrators may call them directly instead of the REST shim — the
tradeoff is camelCase field names (`orgId`, not `org_id`) and the `shepherd.mgmt.v1.<Service>/<Method>`
path shape rather than REST resource paths. Both surfaces share the same session-cookie authorization
(org membership + role) — there is no separate API-token auth for this endpoint family; agent
tokens remain scoped to the `collector.v1` fleet protocol only. Example — listing pipelines for an
org via the Connect endpoint directly, with an authenticated session cookie already in `cookies.txt`:

```bash
curl -s -X POST http://localhost:8080/shepherd.mgmt.v1.PipelineService/ListPipelines \
  -H 'Content-Type: application/json' \
  -H 'X-Requested-With: XMLHttpRequest' \
  -b cookies.txt \
  -d '{"orgId":"<org-uuid>"}'
```

---

## Building a pipeline

Three routes into the same merge engine, in increasing order of control:

1. **Wizards** — guided forms for the six common jobs (app observability, pod
   logs, cluster metrics, database metrics, blackbox probes, self-monitoring).
   Answer a few questions, preview the generated Alloy, commit.
2. **Visual builder** — drag components onto a canvas, wire them together, and
   watch the Alloy config generate as you go. It validates live and refuses to
   save a graph that would not run.
3. **Raw Alloy** — paste the config yourself. Same validation gate.

Whichever you use, a pipeline carries **matchers** (`cluster="prod-eu-1"`,
`role="metrics"`) that decide which collectors receive it, and it passes a
three-stage validation gate before it is served: syntax, `alloy validate`, and a
merge dry-run against every affected collector's *full* merged config — so a
pipeline that is individually valid but conflicts with an existing one is caught
before any agent sees it.

The [getting-started guide](https://procoduck.github.io/shepherd/docs/getting-started.html#pipeline)
walks through building one end to end.

## Development

| Command | Description |
|---|---|
| `make help` | List all targets and env knobs |
| `make build` | Build web SPA + Go binary |
| `make test` | All Go tests (Docker required — testcontainers) |
| `make e2e` | End-to-end suite (~10 min, Docker) |
| `make e2e-k8s` | Kubernetes suite (kind + Helm install) |
| `make lint` | golangci-lint v2 + repo guards |
| `make fmt` | golangci-lint fmt + gofumpt |
| `make generate` | buf + sqlc + version codegen |
| `make helm-lint` | Helm chart lint |
| `make tools` | Install pinned dev tools (ginkgo, gofumpt, sqlc, buf) |

See `docs/spec.md` for the full specification.

---

## Helm deployment

```bash
helm install shepherd deploy/helm/shepherd \
  --set image.tag=0.3.0 \
  --set existingSecret=shepherd-secrets \
  --set "route.enabled=true" \
  --set "route.hostnames[0]=shepherd.internal"
```

The chart reads a Kubernetes secret (`shepherd-secrets`). Only the first two
keys are required — the server refuses to start without them. The rest are
needed only for the setups described:

| Key | Required? | Description |
|---|---|---|
| `SHEPHERD_DATABASE_URL` | **Yes** | PostgreSQL connection string |
| `SHEPHERD_SECURITY_ENCRYPTION_KEY` | **Yes** | Base64-encoded 32-byte key. Encrypts git credentials and the OIDC client secret at rest — **keep it**; rotating it makes previously stored secrets undecryptable |
| `SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD` | Recommended | Password for the administrator created on first boot. Read only while the users table is empty. Unset means `admin`/`admin`, which cannot do anything until it is changed |
| `SHEPHERD_OIDC_CLIENT_SECRET` | Only when SSO is set in the chart | Omit it to configure SSO from the UI instead |
| `SHEPHERD_GRAPH_CLIENT_SECRET` | Only for Microsoft Entra with the Graph group lookup | Not used by any other provider |

See `deploy/helm/shepherd/values.yaml` for all options.

---

## Single sign-on

Shepherd authenticates humans through OIDC against **any spec-compliant
provider** — there are built-in presets for Microsoft Entra ID, Okta, Google,
Auth0, Keycloak, AWS Cognito, GitLab, authentik, and OneLogin, plus a generic
option for anything else.

**One setting decides where the configuration comes from: `oidc.issuer`.**

| `oidc.issuer` in your chart values | Result |
|---|---|
| Set | The chart owns SSO. The admin UI shows the configuration read-only and refuses writes — a cluster whose identity provider is declared in git is not re-pointed from a browser session |
| Not set | An app admin configures a provider at **Admin → Single sign-on**. It is stored encrypted in the database and takes effect **without a restart** |

### Configuring it from the UI

1. **Get in first.** Sign in as the administrator created on first boot (see
   `SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD`), or as any local user with app-admin
   rights. Local accounts and SSO coexist — configuring SSO does not disable
   them.
2. Sign in and open **Admin → Single sign-on**.
3. Pick your provider. The preset fills in claim names and scopes, and tells you
   what you must configure **in the IdP** for group membership to arrive — the
   most common way to get this wrong, because it fails as a silently empty
   group list rather than an error.
4. Register the shown redirect URL with your provider. Its path must be exactly
   `/auth/callback`.
5. **Test connection** before enabling. It runs OIDC discovery and reports the
   issuer and endpoints it found. It checks the issuer only — the client ID,
   secret, and redirect URL are not exercised until someone actually signs in.
6. Fill in **App admin groups**, then enable. Leave it empty and nobody can
   administer Shepherd through SSO, which is why the form warns you.

### Groups decide access

Whatever your provider puts in the groups claim is matched against
`auth.app_admin_group_ids` and each org's admin/reader group. For Entra those
are object GUIDs; **for every other provider they are whatever your IdP emits**
— usually a group name or a path like `acme/platform`. Set
`auth.app_admin_group_ids` (chart) or App admin groups (UI) to those exact
strings.

Microsoft Entra is the one provider with a second option: Shepherd can resolve
groups through the Microsoft Graph directory API instead of the token claim.
Keep it on for Entra — Entra omits the groups claim entirely once a user is in
more than ~200 groups.

Keep at least one local administrator until you have confirmed an SSO sign-in
lands you with app-admin rights. Local users are not a fallback mode — they are
a supported way to run Shepherd, with or without an identity provider.

---

## Architecture overview

```
Alloy (spoke)  ──remotecfg──▶  Shepherd  ──serves──▶  merged Alloy config
                                  │
                 ┌────────────────┼────────────────────┐
                 ▼                ▼                     ▼
           PostgreSQL         Merge engine          Git GitOps
         (sessions, store)  (matcher + declare)    (repo_links)
```

- **Agent protocol**: Connect RPC (`collector.v1`) over h2c
- **Auth**: OIDC BFF against any spec-compliant provider + cookie sessions; agent token Basic auth
- **Merge engine**: Alertmanager-syntax matchers → declare-wrapped Alloy blocks
- **Validation gate**: 3 stages (syntax, `alloy validate`, merge dry-run)
- **GitOps**: any-git-server repo polling via go-git (`git_credentials`: PAT/basic/SSH/ADO-SP/GitHub-App) → validated pipelines (`source = git`)
- **Beacon** (shipped, on by default): a baseline pipeline served to every claimed collector reports component names and health back to Shepherd. Never config text, never raw samples
- **Tenant identity** (shipped): a property of the org, set once by an application administrator. Route creation reads it from there — it cannot be supplied per request
- **Machine actors** (shipped, RPC only — no UI): service accounts scoped `propose` or `apply`, with every mutating RPC checked individually rather than at one chokepoint; a machine write must name the human it acts for, and that claim is verified against the credential
- **Gateway tier** (*built, not wired*): renders Gateway API `HTTPRoute` only — no Ingress fallback, Standard channel, pinned in `deploy/versions.env` — rewriting a per-tenant prefix and **setting** `X-Scope-OrgID` so clients never choose their own tenant. Proven against a real controller in the kind suite; nothing applies these routes to a cluster yet

### Roles

Every role is reachable two ways — from an identity provider's groups, or
assigned directly to a local user. The two coexist; local accounts are a
supported way to run Shepherd, not a fallback.

| Role | Can |
|---|---|
| Application administrator | Create orgs and **set their tenant id**, claim clusters, issue agent tokens, manage local users |
| Org administrator | Everything within one org: pipelines, destinations, routes, teams |
| Org editor | Author what the org *runs* — pipelines, wizards, simulations — but not what it *is*: destinations, routes, git credentials and teams stay with the org admin |
| Team member | Write only what their team owns (scoped write). Membership comes from an IdP group, an explicit list of local users, or both |
| Org viewer | Read only |
| Service account (`apply`) | Machine writes within one org, acting for the human it was issued to |
| Service account (`propose`) | Reads, validation and proposals — never applies |

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the build/test loop and the two
things that catch people out (committed generated code, and the committed SPA
build). Security issues: please follow [SECURITY.md](SECURITY.md) rather than
opening a public issue.

## Licence

[Apache License 2.0](LICENSE).
