# Shepherd

**[procoduck.github.io/sheperd](https://procoduck.github.io/sheperd/)** — project site and quick start.

Self-hosted **Grafana Alloy fleet manager**. Shepherd serves centralised pipeline configurations to Alloy instances via the `remotecfg` protocol, providing a UI for managing pipelines, destinations, wizards, and GitOps sync from any git server (ADO and GitHub-App auth supported).

It also runs the tenant-aware **gateway and receiver tier** that makes OTLP and browser ingest work without client-side tenant configuration, a **beacon** that reports what each collector is actually running, and a read-plus-propose **MCP interface** for AI agents. See `docs/gateway-tier-plan.md` for that work and its review gates.

---

## Quick start (local dev)

```bash
# 1. Start Postgres
docker run -d --name shepherd-pg \
  -e POSTGRES_DB=shepherd -e POSTGRES_USER=shepherd -e POSTGRES_PASSWORD=shepherd \
  -p 5432:5432 postgres:16-alpine

# 2. Build & run
make build
SHEPHERD_DATABASE_URL=postgres://shepherd:shepherd@localhost:5432/shepherd \
SHEPHERD_SECURITY_ENCRYPTION_KEY=$(openssl rand -base64 32) \
./bin/shepherd migrate up && ./bin/shepherd serve

# 3. Create a token
SHEPHERD_DATABASE_URL=... ./bin/shepherd token create --name dev
```

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
  --set image.tag=0.1.0 \
  --set existingSecret=shepherd-secrets \
  --set "route.enabled=true" \
  --set "route.hostnames[0]=shepherd.internal"
```

The chart requires a Kubernetes secret (`shepherd-secrets`) with:

| Key | Description |
|---|---|
| `SHEPHERD_DATABASE_URL` | PostgreSQL connection string |
| `SHEPHERD_OIDC_CLIENT_SECRET` | Entra app client secret |
| `SHEPHERD_GRAPH_CLIENT_SECRET` | Graph SP client secret |
| `SHEPHERD_SECURITY_ENCRYPTION_KEY` | Base64-encoded 32-byte key |

See `deploy/helm/shepherd/values.yaml` for all options.

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
- **Auth**: OIDC BFF (Entra ID) + cookie sessions; agent token Basic auth
- **Merge engine**: Alertmanager-syntax matchers → declare-wrapped Alloy blocks
- **Validation gate**: 3 stages (syntax, `alloy validate`, merge dry-run)
- **GitOps**: any-git-server repo polling via go-git (`git_credentials`: PAT/basic/SSH/ADO-SP/GitHub-App) → validated pipelines (`source = git`)
- **Gateway tier**: Gateway API `HTTPRoute` only (no Ingress fallback), Standard channel, pinned in `deploy/versions.env`. Routes rewrite a per-tenant prefix and **set** `X-Scope-OrgID` so clients never choose their own tenant
- **Tenant identity**: a property of the org, set once by an application administrator. Route creation reads it from there — it cannot be supplied per request
- **Beacon**: every collector is served a small baseline pipeline that reports component names and health back to Shepherd. Never config text, never raw samples
- **Machine actors**: service accounts scoped `propose` or `apply`, with every mutating RPC checked individually rather than at one chokepoint; a machine write must name the human it acts for, and that claim is verified against the credential

### Roles

| Role | Can |
|---|---|
| Application administrator | Create orgs and **set their tenant id**, claim clusters, issue agent tokens |
| Org administrator | Everything within one org: pipelines, destinations, routes, teams |
| Team member | Write only what their team owns (scoped write) |
| Org reader | Read only |
| Service account (`apply`) | Machine writes within one org, acting for the human it was issued to |
| Service account (`propose`) | Reads, validation and proposals — never applies |
