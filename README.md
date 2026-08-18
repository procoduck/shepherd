# Shepherd

Self-hosted **Grafana Alloy fleet manager**. Shepherd serves centralised pipeline configurations to Alloy instances via the `remotecfg` protocol, providing a UI for managing pipelines, destinations, wizards, and ADO GitOps sync.

---

## Quick start (local dev)

```bash
# 1. Start Postgres
docker run -d --name shepherd-pg \
  -e POSTGRES_DB=shepherd -e POSTGRES_USER=shepherd -e POSTGRES_PASSWORD=shepherd \
  -p 5432:5432 postgres:16-alpine

# 2. Build & run
make build-all
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
(`shepherd.mgmt.v1`; see `docs/api-contract-design.md`). The Connect endpoints are plain HTTP
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
| `make build-all` | Build web SPA + Go binary |
| `make test` | Unit tests (no Docker) |
| `make test-integration` | Integration tests (Docker required) |
| `make e2e` | End-to-end suite (~10 min, Docker) |
| `make lint` | golangci-lint v2 |
| `make fmt` | gofmt |
| `make generate` | buf generate + sqlc generate |
| `make helm-lint` | Helm chart lint |

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
           PostgreSQL         Merge engine          ADO GitOps
         (sessions, store)  (matcher + declare)    (repo_links)
```

- **Agent protocol**: Connect RPC (`collector.v1`) over h2c
- **Auth**: OIDC BFF (Entra ID) + cookie sessions; agent token Basic auth
- **Merge engine**: Alertmanager-syntax matchers → declare-wrapped Alloy blocks
- **Validation gate**: 3 stages (syntax, `alloy validate`, merge dry-run)
- **GitOps**: ADO repo polling → validated pipelines (`source = git`)
