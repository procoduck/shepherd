# IMPLEMENTATION PROMPT — "Shepherd": a Custom Grafana Alloy Fleet Manager

You are implementing a production-grade fleet manager for Grafana Alloy collectors, called **Shepherd**. Follow this specification EXACTLY. Where this document makes a decision, do not substitute your own. Where something is genuinely unspecified, choose the simplest option consistent with this document and leave a `// DECISION:` comment explaining it.

---

## 0. Rules for you (the implementing model)

1. **Do not invent APIs.** The Alloy agent protocol is defined by the proto in §4. The Microsoft Graph and Azure DevOps endpoints are listed in §9/§10. Use exactly those.
2. **Never serve an unvalidated config to an agent.** Every config passes the three-stage validation gate (§8) before being stored as servable.
3. **Write tests as you go.** Backend tests use Ginkgo v2 + Gomega exclusively. Every package in `internal/` gets a `_test` suite. Integration tests use testcontainers-go with a real Postgres.
4. **All SQL goes through sqlc.** No string-built SQL, no ORM. All schema changes go through golang-migrate migration files. Never edit an already-committed migration; add a new one.
5. **Secrets are never logged, never returned by any API, and encrypted at rest** (§7.4).
6. **Small commits per milestone** (§14). After each milestone, all tests must pass: `go build ./... && ginkgo -r`.
7. Go 1.26+, formatted and lint-clean per the **exact** golangci-lint v2 config in §20 (`make lint` green at every milestone). Frontend: TypeScript strict mode, no `any`.
8. Releases are built exclusively through GoReleaser per §21 — no hand-rolled build scripts, no `docker build` outside the GoReleaser flow except the local dev target.

---

## 1. What Shepherd is

Shepherd is a self-hosted replacement for Grafana Cloud Fleet Management. It is:

- A **Connect RPC server** implementing `collector.v1.CollectorService` — the native protocol Grafana Alloy's `remotecfg` block speaks. Alloy instances poll it with `GetConfig`; Shepherd returns a merged, validated Alloy configuration per collector.
- A **management REST API + React SPA** where humans manage collectors, config pipelines, wizards, Git repo links, and destinations.
- A **multi-tenant system**: an *Application Admin* manages orgs and assigns resources; *Org Admins* manage everything inside their org; regular users have **read-only** access to collectors their Entra ID groups are assigned to.
- Integrated with **Microsoft Entra ID** (OIDC login + Microsoft Graph for group resolution) and **Azure DevOps** (Git repos as a config source, authenticated with Entra service principals, ArgoCD-credentials-style).
- Backed by **PostgreSQL** as the single source of state.

### Deployment context (informative)

Shepherd runs in the hub cluster of a hub-spoke EKS platform. ~200+ spoke clusters each run the `k8s-monitoring` Helm chart v4 with four Alloy collectors: `alloy-metrics` (StatefulSet, clustered), `alloy-logs` (DaemonSet), `alloy-singleton`, and `alloy-receiver` (OTel gateway). Each collector pod is configured with:

```yaml
collectors:
  alloy-metrics:
    remoteConfig:
      enabled: true
      url: https://shepherd.example.internal
      pollFrequency: "5m"
      auth:
        type: basic
        username: "<agent-token-id>"
        password: "<agent-token-secret>"
      extraAttributes:
        cluster: "<cluster-name>"
        role: "metrics"          # one of: metrics | logs | singleton | receiver
```

The chart-generated collector ID is `grafana-k8s-monitoring-$(CLUSTER_NAME)-$(NAMESPACE)-$(POD_NAME)` — pod-scoped and ephemeral. Shepherd therefore models a **logical collector** as the tuple `(cluster, role)` and treats individual pods as ephemeral *instances* of it. Resolution is done ONLY from the `cluster` and `role` attributes, never by parsing the ID string.

**Role is a signal contract, not just a label.** Each role in `metrics | logs | singleton | receiver` has a fixed set of observability signals (metrics/logs/traces/profiles) it is allowed to serve — `role=metrics` only ever gets a pipeline that carries metrics, `role=logs` only logs, `role=receiver` carries metrics/logs/traces (OTLP + Faro ingest) but not profiles, and `role=singleton` is unrestricted (self-monitoring legitimately mixes signal kinds). `internal/signals` is the source of truth: `internal/signals.Derive` reads a pipeline's Alloy syntax against the schema artifact's wire types to compute its signal set, and `internal/signals.Policies`/`Enforce` hold the role → allowed-signals table. `internal/merge.WithRoleEnforcement` applies it at config-assembly time — a pipeline whose signals the target role's policy does not allow is excluded from that collector's assembled config rather than served silently mismatched (docs/gateway-tier-plan.md W1; excludes are recorded in `AssembleResult.Exclusions` and the generated config's header comment). As of this writing enforcement covers `internal/mgmtapi`'s write-time paths (validate/preview/recompute-on-save); see `docs/project-status.md` F-SIGNAL-SERVE for the one path it does not yet cover.

Spoke clusters already run local chart-generated config (clusterMetrics, podLogs, etc.). Alloy runs remote config in an **isolated component controller** — remote pipelines cannot reference local components. Consequence: **every remote pipeline must be self-contained**, including its own destination components (`prometheus.remote_write`, `loki.write`, `otelcol.exporter.*`). Credentials inside remote pipelines are never inlined; they use the `remote.kubernetes.secret` component to read secrets that already exist on the spoke cluster (§11.4).

---

## 2. Technology stack (fixed — do not deviate)

| Concern | Choice |
|---|---|
| Language | Go 1.26+ |
| CLI | `github.com/spf13/cobra` |
| Config | `github.com/spf13/viper` (file + env, prefix `SHEPHERD_`) |
| Agent API | `connectrpc.com/connect` (Connect protocol, h2c via `golang.org/x/net/http2/h2c`) |
| Management API | REST/JSON with `github.com/go-chi/chi/v5` |
| DB | PostgreSQL 16 |
| DB access | `github.com/jackc/pgx/v5` pool + `sqlc` generated queries |
| Migrations | `github.com/golang-migrate/migrate/v4` (embedded, run via CLI subcommand) |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| Alloy syntax validation | `github.com/grafana/alloy/syntax` + bundled `alloy` binary (`alloy validate`) |
| Proto codegen | `buf` v2, plugins `protocolbuffers/go` + `connectrpc/go` |
| Backend tests | Ginkgo v2 + Gomega; `testcontainers-go` (Postgres module) for integration |
| Frontend | React 18 + TypeScript + Vite |
| Frontend routing/data | TanStack Router + TanStack Query |
| UI kit | shadcn/ui + Tailwind CSS |
| Code editor | CodeMirror 6 with a custom Alloy language mode (§12.6) |
| Forms | react-hook-form + zod |
| Frontend tests | Vitest + React Testing Library |
| Frontend serving | Built SPA embedded in the Go binary via `go:embed` |
| Container | Distroless-style multi-stage Dockerfile; MUST also copy the `grafana/alloy` binary into the image at `/usr/local/bin/alloy` (needed for validation) |

---

## 3. Repository layout

```
shepherd/
├── cmd/shepherd/main.go            # cobra root
├── internal/
│   ├── cli/                        # cobra commands: serve, migrate, validate, token, version
│   ├── config/                     # viper loading + typed Config struct + validation
│   ├── server/                     # http server assembly, middleware, embedded SPA
│   ├── agentapi/                   # collector.v1 Connect service implementation
│   ├── mgmtapi/                    # chi REST handlers (one file per resource)
│   ├── auth/                       # OIDC flow, sessions, RBAC middleware
│   ├── graph/                      # Microsoft Graph client (user groups, group search)
│   ├── ado/                        # Azure DevOps client (SP token, git items API)
│   ├── gitsync/                    # repo-link polling reconciler
│   ├── store/                      # sqlc output + thin repository wrappers
│   ├── merge/                      # pipeline matching + declare-wrap merge + hashing
│   ├── validate/                   # 3-stage validation gate
│   ├── wizard/                     # wizard definitions + template rendering
│   ├── crypto/                     # AES-GCM secret encryption helpers
│   └── testutil/                   # testcontainers Postgres harness, fixtures
├── proto/collector/v1/collector.proto   # vendored (§4)
├── gen/                            # buf output (committed)
├── internal/migrations/sql/        # 0001_init.up.sql / .down.sql, ...
├── sqlc.yaml
├── buf.yaml / buf.gen.yaml
├── web/                            # React app (Vite root)
│   └── src/{routes,components,lib,api,editor}
├── deploy/
│   ├── Dockerfile.local
│   └── helm/shepherd/              # full chart, see §17
├── e2e/                            # local end-to-end suite, see §18
│   ├── docker-compose.e2e.yaml
│   ├── mockmsft/                   # small Go server mocking Graph + ADO REST endpoints
│   ├── alloy/config.alloy          # real Alloy agent config used in e2e
│   └── e2e_suite_test.go ...       # Ginkgo, build tag `e2e`
├── .golangci.yml                   # golangci-lint v2 config, verbatim from §20
├── .goreleaser.yaml                # GoReleaser v2 config per §21
├── Makefile                        # build, generate, test, lint, dev, helm-lint, e2e, release-snapshot targets
└── README.md
```

---

## 4. The agent protocol (collector.v1) — EXACT contract

Vendor this proto at `proto/collector/v1/collector.proto` (package `collector.v1`; source: `github.com/grafana/alloy-remote-config`, Apache-2.0 — keep the license header). Field numbers are normative:

```proto
syntax = "proto3";
package collector.v1;

service CollectorService {
  rpc GetConfig(GetConfigRequest) returns (GetConfigResponse) {}
  rpc RegisterCollector(RegisterCollectorRequest) returns (RegisterCollectorResponse) {}
  rpc UnregisterCollector(UnregisterCollectorRequest) returns (UnregisterCollectorResponse) {}
}

message GetConfigRequest {
  string id = 1;
  map<string, string> attributes = 2 [deprecated = true];
  string hash = 3;
  map<string, string> local_attributes = 4;
  RemoteConfigStatus remote_config_status = 5;
  EffectiveConfig effective_config = 6;
}

message GetConfigResponse {
  string content = 1;
  string hash = 2;
  bool not_modified = 3;
}

message RegisterCollectorRequest {
  string id = 1;
  map<string, string> attributes = 2 [deprecated = true];
  string name = 3;
  map<string, string> local_attributes = 4;
}
message RegisterCollectorResponse {}

message UnregisterCollectorRequest { string id = 1; }
message UnregisterCollectorResponse {}

message RemoteConfigStatus {
  RemoteConfigStatuses status = 1;
  string error_message = 2;
}
enum RemoteConfigStatuses {
  RemoteConfigStatuses_UNSET = 0;
  RemoteConfigStatuses_APPLIED = 1;
  RemoteConfigStatuses_APPLYING = 2;
  RemoteConfigStatuses_FAILED = 3;
}

message EffectiveConfig {
  AgentConfigMap config_map = 1;
}
message AgentConfigMap {
  map<string, AgentConfigFile> config_map = 1;
}
message AgentConfigFile {
  bytes body = 1;
  string content_type = 2;
}
```

### 4.1 Server behavior

Mount the generated handler at `POST /collector.v1.CollectorService/{Method}` on the main HTTP server, wrapped in h2c so both HTTP/1.1+JSON and HTTP/2+proto work. Read `local_attributes`, falling back to deprecated `attributes` if `local_attributes` is empty.

**`RegisterCollector`:**
1. Authenticate via agent token (§7.3). Reject with `connect.CodeUnauthenticated` on failure.
2. Require attributes `cluster` and `role` (role ∈ `metrics|logs|singleton|receiver`); reject with `CodeInvalidArgument` listing what's missing.
3. Upsert `clusters` row by name (org_id NULL if new — "unclaimed").
4. Upsert logical `collectors` row `(cluster_id, role)`.
5. Upsert `collector_instances` row keyed by the wire `id`: store `name`, full `local_attributes` as JSONB, `last_seen = now()`, plus parsed `collector.version` and `collector.os` attributes if present.

**`GetConfig`:**
1. Authenticate; resolve instance → logical collector exactly as above (upsert too — a GetConfig from an unknown instance must self-register; agents may restart-loop without re-registering).
2. Update `last_seen`; if `remote_config_status` is set, persist `status` + `error_message` on the instance row.
3. If the collector's cluster has no org (unclaimed): return an **empty config** `{content: "", hash: sha256hex(""), not_modified: false}` (or `not_modified: true` if the request hash already equals the empty hash). Never error — an unclaimed collector must keep running its local config undisturbed.
4. Otherwise assemble the served config via the merge engine (§6). The merge engine returns `(content, hash)` from the serve-cache.
5. If `request.hash == hash`: return `{not_modified: true, hash: hash}` with empty content. Else return `{content, hash, not_modified: false}`.

**`UnregisterCollector`:** mark the instance row `unregistered_at = now()`. Do not delete.

**Lifecycle sweeper** (background goroutine, interval from config): mark instances *inactive* if `last_seen` older than `agent.inactive_after` (default `3h`); hard-delete instance rows older than `agent.delete_after` (default `720h`). Logical collectors with zero instances remain (they hold assignments) but display as "no live instances".

### 4.2 Hashing

`hash = hex(sha256(content))` over the exact served string. The agent only echoes this back; consistency matters, algorithm choice is server-side.

---

## 5. Data model (PostgreSQL)

Migration `0001_init` creates (all `id` are `uuid DEFAULT gen_random_uuid()` unless noted; all tables get `created_at`/`updated_at` timestamptz):

```sql
orgs(id, name text UNIQUE, display_name text,
     admin_group_id text,          -- Entra group object ID (GUID) whose members are Org Admins
     reader_group_id text NULL)    -- optional org-wide reader group

clusters(id, name text UNIQUE, org_id uuid NULL REFERENCES orgs)  -- NULL = unclaimed

collectors(id, cluster_id uuid REFERENCES clusters, role text CHECK (role IN ('metrics','logs','singleton','receiver')),
           UNIQUE(cluster_id, role))

collector_instances(id text PRIMARY KEY,      -- the remotecfg wire id
           collector_id uuid REFERENCES collectors, name text,
           local_attributes jsonb, alloy_version text, os text,
           last_seen timestamptz, unregistered_at timestamptz NULL,
           remote_config_status text, remote_config_error text)

group_assignments(id, collector_id uuid REFERENCES collectors,
           group_id text, group_display_name text,   -- Entra GUID + cached name
           UNIQUE(collector_id, group_id))

destinations(id, org_id uuid REFERENCES orgs, name text, 
           type text CHECK (type IN ('prometheus','loki','otlp')),
           url text, tenant_id text,
           secret_name text, secret_namespace text,  -- k8s secret on the SPOKE, read via remote.kubernetes.secret
           auth_mode text CHECK (auth_mode IN ('none','oauth2_secret','basic_secret')),
           extra jsonb, UNIQUE(org_id, name))

pipelines(id, org_id uuid REFERENCES orgs, name text,
           contents text, matchers jsonb,            -- array of matcher strings, §6.1
           enabled boolean DEFAULT false,
           source text CHECK (source IN ('ui','wizard','git')),
           wizard_kind text NULL, wizard_state jsonb NULL,   -- for re-opening wizards
           repo_link_id uuid NULL,                   -- set when source='git'
           git_path text NULL,
           created_by text, updated_by text,
           UNIQUE(org_id, name))

pipeline_revisions(id, pipeline_id uuid REFERENCES pipelines ON DELETE CASCADE,
           revision int, contents text, matchers jsonb, enabled boolean,
           changed_by text, changed_at timestamptz, change_note text,
           UNIQUE(pipeline_id, revision))

ado_credentials(id, org_id uuid REFERENCES orgs, name text,
           ado_org_url text,                         -- https://dev.azure.com/{org}
           entra_tenant_id text, client_id text,
           client_secret_enc bytea,                  -- AES-GCM encrypted, §7.4
           UNIQUE(org_id, name))

repo_links(id, org_id uuid REFERENCES orgs,
           collector_id uuid REFERENCES collectors,  -- a repo link targets ONE logical collector
           credential_id uuid REFERENCES ado_credentials,
           project text, repository text, branch text DEFAULT 'main',
           path text DEFAULT '/',                    -- directory containing *.alloy files
           poll_interval_seconds int DEFAULT 180,
           last_synced_at timestamptz, last_commit text,
           sync_status text, sync_error text)

agent_tokens(id, name text, token_hash bytea,        -- sha256 of secret; secret shown once at creation
           created_by text, revoked_at timestamptz NULL)

serve_cache(collector_id uuid PRIMARY KEY REFERENCES collectors,
           content text, hash text, computed_at timestamptz,
           dirty boolean DEFAULT true)

sessions(id text PRIMARY KEY,                       -- random 256-bit, base64url
           user_oid text, email text, display_name text,
           group_ids jsonb, is_app_admin boolean,
           id_token_expires timestamptz, expires_at timestamptz)

audit_log(id bigserial, at timestamptz DEFAULT now(), actor text, actor_type text,
           org_id uuid NULL, action text, resource_type text, resource_id text, detail jsonb)
```

Indexes: `collector_instances(collector_id, last_seen)`, `pipelines(org_id, enabled)`, `sessions(expires_at)`, `audit_log(org_id, at)`.

Every mutating management-API handler writes an `audit_log` row.

---

## 6. Merge engine (`internal/merge`)

### 6.1 Matchers

A pipeline has a list of matcher strings using **Prometheus Alertmanager matcher syntax**: `cluster="prod-eu-1"`, `role!="logs"`, `cluster=~"prod-.*"`, `env!~"dev.*"`. Parse them with `github.com/prometheus/alertmanager/pkg/labels.ParseMatcher`. All matchers on a pipeline are **ANDed**. They evaluate against the label set of the target logical collector: `{cluster: <cluster name>, role: <role>}` plus every key/value from the union of that collector's instances' `local_attributes` (last-seen instance wins per key). A pipeline with zero matchers matches **nothing** (safety default — the UI must always require at least one matcher).

Pipelines with `source='git'` skip matching: they apply to exactly their `repo_link.collector_id`.

### 6.2 Assembly algorithm (per logical collector)

```
inputs: collector C (org O), all pipelines P of org O where enabled = true
selected := git pipelines whose repo_link.collector_id == C.id
         ++ ui/wizard pipelines whose matchers match C's label set
sort selected by name (stable, deterministic)
for each p in selected:
    block_name := "pipe_" + sanitize(p.name)      // [a-z0-9_], must start with letter
    emit:
        declare "<block_name>" {
          <p.contents, indented one level>
        }
        <block_name> "default" { }
content := header comment (shepherd version, collector id, generated-at RFC3339, list of pipeline names+revisions)
         + concatenation of all emitted blocks separated by blank lines
hash := hex(sha256(content))
```

`declare`-wrapping namespaces each pipeline's components, so two pipelines may both contain `prometheus.remote_write "default"` without collision. Pipelines must therefore be written as **standalone** snippets (docs + wizard templates guarantee this).

### 6.3 Serve cache & invalidation

`GetConfig` reads from `serve_cache`. A row is recomputed (and re-validated at stage 3, §8) when `dirty = true` or missing. Mark dirty for all potentially affected collectors whenever: a pipeline is created/updated/deleted/toggled (dirty every collector in the org — cheap and correct), a git sync changes files (dirty that collector), a cluster is claimed/unclaimed, or a matcher-relevant attribute changes on registration. Recomputation happens lazily inside `GetConfig` with a per-collector `singleflight` guard. If recomputation fails validation, KEEP the previous cache entry, log an error, and record the failure in `audit_log` — never serve a broken config and never serve empty because of a merge bug.

---

## 7. AuthN/AuthZ

### 7.1 OIDC (human users) — standard BFF code flow

- Shepherd is a **confidential client** at a spec-compliant OIDC issuer. Entra ID is the reference deployment, but any provider that serves a discovery document works — see §7.1a for the per-provider knobs and §7.1b for where the configuration comes from.
- Viper config: `oidc.issuer` (Entra: `https://login.microsoftonline.com/<tenant>/v2.0`), `oidc.client_id`, `oidc.client_secret`, `oidc.redirect_url`, `oidc.scopes` (default `openid profile email offline_access GroupMember.Read.All`).
- `GET /auth/login` → generate `state` + PKCE verifier, store in a short-lived httpOnly cookie, redirect to the authorize endpoint.
- `GET /auth/callback` → verify state, exchange code (with PKCE) for tokens, verify ID token with go-oidc. Resolve the user's groups per §7.1a. Store the session row (§5) with `group_ids`, set session cookie: httpOnly, `Secure`, `SameSite=Lax`, name `shepherd_session`. Session TTL: `auth.session_ttl` (default 8h, sliding).
- `GET /auth/me` → current user profile + computed roles. `POST /auth/logout` → delete session.
- `GET /auth/methods` → which sign-in methods the login page should offer, plus the label for the OIDC button. It reports whether OIDC is **live** (a discovered provider is loaded), not merely whether one is configured: a saved-but-undiscoverable provider must not render a button that can only dead-end.
- All `/api/*` routes require a valid session (middleware). CSRF: require header `X-Requested-With: XMLHttpRequest` on mutating requests (sufficient with SameSite=Lax).

### 7.1a Provider portability — claims and groups

Which claim carries the subject, the email, the display name, and the group list is per-provider configuration, not a constant. `internal/auth.Presets` holds the built-in catalogue (Entra, Okta, Google, Auth0, Keycloak, Cognito, GitLab, authentik, OneLogin, generic); a preset only supplies **defaults** — every value it prefills is stored explicitly, so the effective configuration is readable without consulting the preset table.

- **Subject** — `oidc.subject_claim`, default `oid` (Entra's immutable object ID, what this code has always read). Every other provider needs `sub`. Falls back to the ID token's spec-mandated `sub` when the configured claim is absent, so a mistyped setting degrades to a working identity rather than an empty `user_oid`.
- **Groups** — `oidc.groups_claim`, default `groups` (`cognito:groups` for Cognito, `groups_direct` for GitLab, a namespaced URI for Auth0). Values are matched against `auth.app_admin_group_ids` and against each org's `admin_group_id` / `reader_group_id`; for a non-Entra provider those columns therefore hold whatever the IdP emits (usually a group name or path), not a GUID. Nothing in the schema ever assumed a GUID — only the Entra-shaped documentation did. The reader accepts all three shapes providers use: a JSON array, a bare string, or a space-separated list.
- **Microsoft Graph** — `oidc.use_graph_groups` (default: true when `oidc.provider` is `entra`, false otherwise) keeps the transitive lookup `GET {graph_base_url}/v1.0/me/transitiveMemberOf/microsoft.graph.group?$select=id,displayName&$top=999` (follow `@odata.nextLink`). It stays the default for Entra because Entra omits the groups claim entirely on >200-group overage. `graph.base_url` is pinned to Microsoft's own Graph hosts (global plus the sovereign clouds) on the UI-managed path: the signing-in user's **delegated access token** is sent to it as a bearer credential, so an unconstrained value would let an app admin redirect every user's token to a collector they control.
  - **Behavioral change, deliberate:** when the Graph call fails, the groups claim is now used as a fallback. Previously a Graph error yielded *no* groups, which silently stripped every administrator of access during a Graph outage or a revoked `GroupMember.Read.All` consent. The claim is signed by the same IdP so it cannot be forged, but it is **not the same set** — claim scope is configured per app registration ("all groups" vs "groups assigned to the application"), and AD-synced groups may emit `sAMAccountName` rather than GUIDs. An Entra deployment that wants the old all-or-nothing behavior must leave the groups claim unmapped in its app registration.

### 7.1b Configuration source — chart, or the UI

OIDC configuration has exactly two sources, and **`oidc.issuer` decides which**:

- **Set in the chart / environment** → chart config wins. The admin UI shows the values read-only and refuses writes (`FailedPrecondition`). A cluster whose identity provider is declared in git must not be silently re-pointed by whoever holds an app-admin session.
- **Not set** → an app admin configures a provider from the UI (Admin → Single sign-on), persisted to the singleton `oidc_settings` row (migration 0014). The client secret is AES-256-GCM-encrypted at rest via `internal/crypto`, following `git_credentials.client_secret_enc`'s precedent, and is never returned by the API — `GetOidcSettings` answers `client_secret_set` only. The local-admin break-glass account (§7.4) is the bootstrap path.

Saving takes effect **without a restart**: `auth.Handler` holds the discovered provider and oauth2 client behind an atomic pointer that `Reload` swaps, and `/auth/login` + `/auth/callback` are mounted unconditionally (they redirect to `/login?auth_error=oidc_not_configured` when no provider is live). Other replicas pick the change up within 30s via a staleness check on the auth paths. Discovery runs **before** the write whenever the configuration is being enabled, so a provider that cannot discover is never stored as live — the admin who would have to fix it may be relying on that same login page.

`AdminService` carries the surface, app-admin only, with no org-scoped variant: this configuration decides who can hold an app-admin session in the first place, so delegating any part of it would let an org admin widen their own reach. `TestOidcSettings` probes a candidate configuration without storing it and reports the discovered issuer, endpoints, advertised scopes, and PKCE support — a failed probe comes back in the response body, not as an RPC error, so the form renders the reason inline. It probes **discovery only**: it never exercises the client ID, secret, or redirect URL, so a green result means "this issuer is real", not "sign-in will work".

**Discovery is a deliberately constrained fetch** (`internal/auth/discovery.go`). It is the one place an authenticated user picks a URL the *server* then retrieves, which makes it an SSRF primitive unless it is bounded — and app admin is an application role, not cluster-admin. Four constraints, each closing a distinct hole:

1. A 10s client timeout. `go-oidc` uses `http.DefaultClient`, which has none; `Reload` holds `reloadMu` across the fetch and three *unauthenticated* routes can trigger one, and it also runs before `ListenAndServe`, so an unbounded fetch is both a goroutine pile-up and a boot hang.
2. A `net.Dialer.Control` guard rejecting loopback, private, link-local, CGNAT, and unspecified addresses — applied **after** DNS resolution, so a hostname resolving to `169.254.169.254` and DNS rebinding are both covered.
3. Redirects restricted to `https`, capped at 5 hops.
4. **Discovery errors never carry the response body.** `go-oidc`'s own error is `fmt.Errorf("%s: %s", resp.Status, body)`; surfacing that through `TestOidcSettings` would turn "an admin can probe a URL" into "an admin can read it". Shepherd fetches and parses the document itself and reports status codes only, then builds the provider from `oidc.ProviderConfig` so the constrained client also governs JWKS fetches for the provider's lifetime.

Background refreshes run on a context **detached from the request** (`context.WithoutCancel` + its own timeout): `Reload` spends the 30s backoff before it attempts anything, so inheriting cancellation would let an anonymous client that aborts `/auth/methods` every 30s keep a replica from ever activating OIDC.

### 7.2 Roles

- **App Admin**: user has any group in `auth.app_admin_group_ids` (viper list). Can do everything, everywhere: CRUD orgs, claim/unclaim clusters into orgs, CRUD group assignments, CRUD agent tokens, view all.
- **Org Admin** of org O: member of `O.admin_group_id`. Full CRUD within O: pipelines, destinations, ADO credentials, repo links, wizards, group assignments *for O's collectors*. Cannot touch other orgs, cannot claim clusters, cannot manage agent tokens.
- **Reader**: for any collector C, a user can *view* C (and the pipelines/served config affecting C) if they are a member of any group in `group_assignments` for C, or of the org's `reader_group_id`, or hold a higher role. Readers can create nothing.

Implement as chi middleware helpers: `RequireAppAdmin`, `RequireOrgAdmin(orgIDParam)`, `RequireCollectorRead(collectorIDParam)`. Every handler declares exactly one.

### 7.3 Agent tokens (machine auth)

Agents authenticate `collector.v1` calls with HTTP Basic auth: username = token UUID, password = 32-byte random secret (base64url). Store only `sha256(secret)`; compare with `crypto/subtle.ConstantTimeCompare`. App Admin creates/revokes tokens via API/UI; the secret is displayed exactly once. Implement as a Connect interceptor. (v1 tokens are global-authN only; tenancy comes from cluster claiming.)

### 7.4 Secret encryption at rest

`internal/crypto`: AES-256-GCM with key from viper `security.encryption_key` (32 bytes, base64; from env/k8s Secret). Random 12-byte nonce prepended to ciphertext. Used for `ado_credentials.client_secret_enc`. Refuse to boot without a valid key.

---

## 8. Validation gate (`internal/validate`) — runs before ANY save or serve

**Stage 1 — Syntax.** Parse with `github.com/grafana/alloy/syntax/parser`. On error, return structured diagnostics `{line, col, message}` (the parser gives positions). This runs on: pipeline save, wizard output, every git-synced file, and the merged output.

**Stage 2 — Semantic (`alloy validate`).** Write the candidate to a temp file and exec the bundled binary: `alloy validate --stability.level=experimental <file>` (stability level from viper `validate.stability_level`, default `experimental` to match the fleet). Timeout 10s. Parse stderr into diagnostics. This catches unknown components, bad attribute types, unset required attributes. Run it on the **declare-wrapped form** of the pipeline (wrap exactly as the merge engine would, §6.2) so what's validated is what ships.

**Stage 3 — Merge dry-run.** For a pipeline save/toggle: compute the set of logical collectors the new matcher set would affect (plus those affected by the *old* version), assemble each collector's full merged config with the candidate change applied, and run stages 1–2 on each merged result. If ANY merged config fails, **reject the save** with a response listing the failing collectors and diagnostics. This prevents the "one bad pipeline poisons the whole merge" failure mode.

API: `POST /api/orgs/{org}/pipelines/validate` runs stages 1–2 only (fast, used by the editor's Validate button and on-type debounce); the actual save/update/enable endpoints run all three stages server-side regardless of what the client did.

Git-synced files that fail validation: keep the previous good pipeline revision, set `repo_links.sync_status='error'` with details, surface in UI. Never partially apply a commit: a sync applies all files of a commit transactionally or none.

---

## 9. Microsoft Graph (`internal/graph`)

Two client modes:

1. **Delegated** (user's access token from login): only `/me/transitiveMemberOf` as in §7.1.
2. **Application** (client credentials with the same app registration; requires `Group.Read.All` application permission, admin-consented): used by admin UIs to search groups by display name: `GET /v1.0/groups?$filter=startswith(displayName,'{q}')&$select=id,displayName&$top=20`. Endpoint: `GET /api/admin/groups/search?q=...` (App Admin or Org Admin). Cache results in-process for 5 minutes.

Token acquisition: plain `golang.org/x/oauth2/clientcredentials` against `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token`, scope `https://graph.microsoft.com/.default`. Do not add heavyweight SDKs; call Graph with `net/http` + typed structs.

---

## 10. Azure DevOps integration (`internal/ado`, `internal/gitsync`)

> **SUPERSEDED (2026-08-19) by `docs/git-provider-design.md`.** This section is ADO-only:
> it specifies the Azure DevOps REST API as the transport. The requirement has changed to
> **standard git against any host**, with Entra service principals as one authentication
> mode among several, and Gitea as the test server. The credential model, polling cadence,
> validation, and pipeline/revision/audit semantics below still hold; the transport,
> `ado_credentials`/`repo_links` shape, and the ADO-specific REST calls do not.
> Tracked as F9 in `docs/project-status.md`.

**Credential model (ArgoCD-style):** Org Admins register named credentials (`ado_credentials`) — an Entra **service principal** (tenant ID, client ID, client secret) plus the ADO org URL. Multiple credentials per org. The SP must be added as a user in the ADO org with repo Read access.

**Token:** client-credentials grant, scope `499b84ac-1321-427f-aa17-267ca6975798/.default` (the Azure DevOps resource ID — this exact GUID). Cache tokens until 5 min before expiry.

**API calls** (REST 7.1, `Authorization: Bearer <token>`):
- Verify/link: `GET {org}/{project}/_apis/git/repositories/{repo}?api-version=7.1`
- Latest commit: `GET .../repositories/{repo}/commits?searchCriteria.itemPath={path}&searchCriteria.$top=1&searchCriteria.itemVersion.version={branch}&api-version=7.1`
- List files: `GET .../repositories/{repo}/items?scopePath={path}&recursionLevel=OneLevel&versionDescriptor.version={branch}&api-version=7.1`
- File content: `GET .../repositories/{repo}/items?path={file}&includeContent=true&versionDescriptor.version={branch}&api-version=7.1`

**Sync reconciler (`gitsync`):** one goroutine ticks every 15s, picks due repo_links (`last_synced_at + poll_interval < now`), processes each: get latest commit for path@branch; if unchanged, touch `last_synced_at` and done. Else fetch all `*.alloy` files under path, validate each (stages 1–2), then upsert pipelines: `name = <linkslug>/<filename-without-ext>`, `source='git'`, `repo_link_id`, `enabled=true`, bump revision if content changed; delete git pipelines of this link whose file disappeared; run stage-3 merge validation for the target collector with the whole new set — on failure, roll the transaction back, keep old state, set `sync_status='error'`. On success set `last_commit`, `sync_status='ok'`, dirty the serve cache.

Git pipelines are **read-only in the UI** (view + revision history only; edits happen in the repo). Git and UI/wizard pipelines coexist and merge together per §6.2.

---

## 11. Wizards (`internal/wizard`)

A wizard is a typed, versioned generator: input schema (zod on the client, mirrored Go struct with validation on the server) → rendered Alloy pipeline(s) via `text/template` templates embedded in the binary. Saving a wizard result creates normal `pipelines` rows with `source='wizard'`, `wizard_kind`, and `wizard_state` (the raw input JSON) so it can be re-opened, edited in the wizard, and re-rendered (creating a new revision). Org Admins may also "detach" a wizard pipeline (converts to `source='ui'`, freeing raw editing, one-way).

### 11.1 Wizard #1 (v1 scope): **Application Observability**

Purpose: scrape logs and/or metrics from selected namespaces on selected clusters, shipping to org destinations.

**Steps (server enforces the same rules):**
1. **Scope** — pick one or more claimed clusters of the org (multi-select). Pick signal: Metrics, Logs, or Both.
2. **Namespaces** — enter namespaces as chips (free text, RFC 1123 validated). Choice: `include listed` or `exclude listed`.
3. **Metrics options** (if metrics): annotation-based discovery using `prometheus.io/scrape|path|port|scheme` annotations on pods; optional extra `metric_relabel` keep-regex for metric names; scrape interval (default 60s).
4. **Logs options** (if logs): include/exclude container name regex; optional multiline; optional JSON `level` label extraction.
5. **Destinations** — pick one org destination of type `prometheus` (for metrics) and/or `loki` (for logs).
6. **Review** — show the exact rendered Alloy config per generated pipeline (read-only CodeMirror), show which logical collectors will match, run validation (stages 1–2) live; Save & Enable / Save disabled.

**Generation:** one metrics pipeline per run matched to `role="metrics"` + a `cluster=~"(c1|c2|…)"` matcher; one logs pipeline matched to `role="logs"` likewise. Pipeline names: `appobs-<slug>-metrics` / `-logs`.

### 11.2 Metrics template (shape — implement exactly this structure)

```alloy
discovery.kubernetes "pods" {
  role = "pod"
  namespaces { names = [{{ namespaces }}] }
}
discovery.relabel "annotated" {
  targets = discovery.kubernetes.pods.targets
  rule {
    source_labels = ["__meta_kubernetes_pod_annotation_prometheus_io_scrape"]
    regex = "true"
    action = "keep"
  }
  // + standard path/port/scheme rewrite rules from the annotations
}
prometheus.scrape "app" {
  targets         = discovery.relabel.annotated.output
  scrape_interval = "{{ interval }}"
  forward_to      = [prometheus.remote_write.dest.receiver]
}
{{ if extra keep-regex }}/* prometheus.relabel stage between scrape and write */{{ end }}
remote.kubernetes.secret "dest" {
  name      = "{{ destination.secret_name }}"
  namespace = "{{ destination.secret_namespace }}"
}
prometheus.remote_write "dest" {
  endpoint {
    url = convert.nonsensitive(remote.kubernetes.secret.dest.data["url"]) + "/api/v1/push"
    headers = { "X-Scope-OrgID" = "{{ destination.tenant_id }}" }
    // auth block per destination.auth_mode, credentials from the secret's keys
    tls_config { insecure_skip_verify = true }
  }
}
```

### 11.3 Logs template (shape)

`discovery.kubernetes` (role pod, namespaces) → `discovery.relabel` (namespace include/exclude, container regex, standard k8s label mapping) → `loki.source.kubernetes` → optional `loki.process` (multiline / JSON level stage) → `remote.kubernetes.secret` + `loki.write` with `url = convert.nonsensitive(...data["url"]) + "/loki/api/v1/push"`, `tenant_id`, auth per destination.

### 11.4 Destination credential convention (document in README)

Destinations reference a Kubernetes Secret that must already exist on every spoke cluster (the platform already distributes such secrets — e.g. `mimir`/`loki` secrets containing `url` and OAuth client credentials). Shepherd stores only the secret's name/namespace and non-sensitive metadata. Rendered configs read it at runtime via `remote.kubernetes.secret`, so **no telemetry-backend credential ever enters Shepherd's database or the served config text**.

---

## 12. Management REST API (`/api`, JSON)

Standard envelope: errors as `{"error": {"code": "...", "message": "...", "details": [...]}}`; lists support `?limit=&offset=` and return `{"items": [...], "total": n}`. Resources (roles in brackets):

```
GET    /api/me                                   [any]  profile + roles + orgs visible
# Admin
POST   /api/admin/orgs                           [app]  {name, display_name, admin_group_id, reader_group_id?}
GET    /api/admin/orgs                           [app]
PATCH  /api/admin/orgs/{org}                     [app]
DELETE /api/admin/orgs/{org}                     [app]  (only if empty)
GET    /api/admin/clusters?unclaimed=true        [app]  discovery of registered clusters
POST   /api/admin/clusters/{cluster}/claim       [app]  {org_id}
POST   /api/admin/clusters/{cluster}/unclaim     [app]
GET    /api/admin/agent-tokens                   [app]
POST   /api/admin/agent-tokens                   [app]  -> secret returned ONCE
DELETE /api/admin/agent-tokens/{id}              [app]  (revoke)
GET    /api/admin/groups/search?q=               [app|orgadmin]  Graph-backed
# Org-scoped
GET    /api/orgs/{org}/collectors                [reader+]  list logical collectors + instance rollups
GET    /api/orgs/{org}/collectors/{id}           [reader]   detail incl. instances, status, matched pipelines
GET    /api/orgs/{org}/collectors/{id}/served-config [reader]  current cache content+hash
POST   /api/orgs/{org}/collectors/{id}/assignments   [orgadmin] {group_id}
DELETE /api/orgs/{org}/collectors/{id}/assignments/{group_id} [orgadmin]
GET    /api/orgs/{org}/pipelines                 [reader]
POST   /api/orgs/{org}/pipelines                 [orgadmin] (validation gate)
GET    /api/orgs/{org}/pipelines/{id}            [reader]   incl. revisions
PUT    /api/orgs/{org}/pipelines/{id}            [orgadmin] (validation gate)
POST   /api/orgs/{org}/pipelines/{id}/enable|disable [orgadmin] (stage-3 on enable)
DELETE /api/orgs/{org}/pipelines/{id}            [orgadmin]
POST   /api/orgs/{org}/pipelines/validate        [orgadmin] stages 1–2, returns diagnostics
GET    /api/orgs/{org}/pipelines/{id}/preview-matches [orgadmin] collectors a matcher set hits
GET    /api/orgs/{org}/attributes                [orgadmin] distinct attribute keys → sorted distinct values across the org's collector instances (incl. built-ins cluster/role); feeds matcher autocomplete
CRUD   /api/orgs/{org}/destinations              [orgadmin write, reader read]
CRUD   /api/orgs/{org}/ado-credentials           [orgadmin]  (secret write-only)
POST   /api/orgs/{org}/ado-credentials/{id}/test [orgadmin]  verifies token + org access
CRUD   /api/orgs/{org}/repo-links                [orgadmin]
POST   /api/orgs/{org}/repo-links/{id}/sync      [orgadmin]  force immediate sync
GET    /api/orgs/{org}/wizards/application-observability/schema  [orgadmin]
POST   /api/orgs/{org}/wizards/application-observability/render  [orgadmin] input -> rendered configs + diagnostics + match preview
POST   /api/orgs/{org}/wizards/application-observability/commit  [orgadmin] input -> creates pipelines (gate)
GET    /api/orgs/{org}/audit                     [orgadmin]
```

---

## 13. Frontend — explicit layout, design system, and editor spec

This section is prescriptive. Do not restyle, do not pick different components, do not "improve" the visual design. Every screen uses ONLY shadcn/ui components + Tailwind utilities with the tokens below.

### 13.1 Design system (fixed tokens)

- **Theme**: dark mode is the DEFAULT; light mode available via a toggle in the user menu (persist in `localStorage`, apply via the `class` strategy on `<html>`). All colors below are Tailwind palette names — implement both modes with shadcn's CSS-variable theming; do not hand-pick hex values elsewhere.
- **Neutrals**: `zinc`. Dark mode: page background `zinc-950`, surface/card `zinc-900`, subtle borders `zinc-800`, primary text `zinc-100`, secondary text `zinc-400`. Light mode: `white` / `zinc-50` / `zinc-200` / `zinc-900` / `zinc-500`.
- **Accent**: `indigo-500` (hover `indigo-400` dark / `indigo-600` light). Used ONLY for: primary buttons, active nav item indicator, focused inputs, links, active stepper step, selected tabs. Nothing else is indigo.
- **Status colors** (used ONLY in badges, dots, and diagnostic text): success `emerald-500`, warning `amber-500`, error `red-500`, info `sky-500`, neutral/unknown `zinc-500`.
- **Typography**: UI font **Inter** (self-hosted via `@fontsource-variable/inter`); monospace **JetBrains Mono** (`@fontsource-variable/jetbrains-mono`) for ALL code, hashes, IDs, attribute values, and token secrets. Sizes: page title `text-xl font-semibold`; section/card title `text-sm font-medium`; body `text-sm`; table text `text-sm`; captions/meta `text-xs text-zinc-400`. Never larger than `text-xl`.
- **Density & shape**: spacing scale multiples of 4px; cards/panels `rounded-lg border` (border color per theme) with `p-4` (stat cards) or `p-6` (forms); NO drop shadows in dark mode, `shadow-sm` only in light mode; buttons/inputs default shadcn `size="sm"` throughout (this is a dense operator tool); table rows `h-10`.
- **Icons**: `lucide-react` only, `size={16}` inline / `size={18}` nav. Role icons (fixed): metrics → `Gauge`, logs → `ScrollText`, singleton → `Box`, receiver → `Waypoints`.
- **Badges** (shadcn Badge, variant per status): Applied → emerald outline dot + "Applied"; Applying → amber + "Applying"; Failed → red + "Failed"; Unknown/Unset → zinc + "Unknown"; source badges: `wizard` (sky), `git` (violet, icon `GitBranch`), `ui` (zinc); Enabled/Disabled as filled emerald / outline zinc.
- **Motion**: only shadcn/Radix built-in transitions. No custom animation, no page transitions.

### 13.2 App shell geometry

Full-height flex layout, no page scroll except the content area:

```
┌──────────┬──────────────────────────────────────────────┐
│ sidebar  │ topbar (h-14, border-b, px-6)                │
│ w-60     ├──────────────────────────────────────────────┤
│ border-r │ content (flex-1, overflow-y-auto)            │
│          │   max-w-[1400px] mx-auto px-6 py-6           │
└──────────┴──────────────────────────────────────────────┘
```

- **Sidebar** (`w-60`, collapsible to `w-14` icon-only via a chevron button at its bottom; persist state): top = product mark — `Shield` icon in indigo + wordmark "Shepherd" (`text-sm font-semibold tracking-tight`). Nav groups with `text-xs uppercase text-zinc-500` group labels: *(no label)*: Overview; **Fleet**: Collectors, Pipelines, Wizards; **Delivery**: Destinations, Git; **Admin** (App Admin only): Orgs, Clusters, Agent Tokens; *(bottom, above collapse)*: Audit. Items: `h-9 rounded-md px-3 text-sm` with icon; active item `bg-zinc-800/60 text-zinc-100` (dark) plus a `w-0.5` indigo bar on the left edge; inactive `text-zinc-400 hover:text-zinc-100 hover:bg-zinc-800/40`.
- **Topbar**: left = breadcrumbs (shadcn Breadcrumb, auto-derived from the route). Right, in order: **org switcher** (shadcn Combobox button showing current org `display_name` with `Building2` icon; App Admin additionally has an "All orgs" pseudo-entry on Collectors/Audit), theme toggle (`Sun`/`Moon` ghost icon button), user menu (Avatar with initials → dropdown: name + email header, role badges — "App Admin" indigo / "Org Admin" emerald / "Reader" zinc — then Log out).
- Selected org is part of the URL state via a `?org=` search param managed by TanStack Router; switching org preserves the current route.

### 13.3 Shared patterns (use everywhere, never ad-hoc)

- **Tables**: shadcn Table + TanStack Table v8. Sticky header row (`bg` = surface), sortable columns show `ArrowUpDown`, hover row `bg-zinc-900/60`, whole row clickable when a detail page exists (`cursor-pointer`, plus an explicit chevron cell). Column of actions = right-aligned ghost `MoreHorizontal` dropdown. Pagination footer: "1–25 of 214" + Prev/Next, page size fixed 25.
- **Filter bar** above every table: left-aligned row of controls `gap-2` — a `w-64` search Input with `Search` icon, then Select/Combobox filters, then active filter chips (dismissible Badge), right-aligned primary action Button (e.g. "New pipeline"). Filters sync to URL search params.
- **Loading**: shadcn Skeleton — tables render 5 skeleton rows; cards render skeleton blocks. Never a full-page spinner.
- **Empty states**: centered in the content card, `py-16`: muted icon (size 32), one bold line, one `text-zinc-400` line, one primary action. Exact copy per screen is given in §13.5.
- **Errors**: query errors render an inline Alert (destructive) with the server `error.message` and a Retry button — never a toast for load failures. Mutation results DO use toasts (shadcn `sonner`): success = short confirmation; failure = error message. Validation failures render inline (§13.6), not as toasts.
- **Dialogs**: shadcn Dialog for create/edit forms (`max-w-lg`); AlertDialog for destructive confirms — title "Delete <name>?", body states blast radius, confirm button destructive variant, and for high-risk actions (unclaim cluster, delete pipeline, revoke token) require typing the resource name into an input to enable the button.
- **Forms**: react-hook-form + zod + shadcn Form primitives; labels above inputs, `text-xs text-zinc-400` help text below, field errors in red-500 `text-xs`. Submit = primary right-aligned; Cancel = ghost left of it.
- **Timestamps**: always relative ("4m ago") with absolute ISO on hover (Tooltip). **Hashes/IDs**: JetBrains Mono, first 10 chars + copy-on-click button (`Copy` icon, toast "Copied").

### 13.4 Route tree

```
/login                      — sign-in page
/                           — Overview
/collectors                 — Collectors list
/collectors/:id             — Collector detail (tabs: Instances | Served Config | Pipelines | Access)
/pipelines                  — Pipelines list
/pipelines/new              — Editor (create)
/pipelines/:id              — Editor (view/edit; read-only for readers & git-sourced)
/wizards                    — Wizard gallery (cards, one per registered wizard)
/wizards/{kind}             — Wizard stepper (one generic runner for every registered wizard)
/destinations               — Destinations list + create/edit dialogs
/git                        — Tabs: Repo links | Credentials
/admin/orgs                 — [app admin] Orgs
/admin/clusters             — [app admin] Cluster claiming
/admin/tokens               — [app admin] Agent tokens
/audit                      — Audit log (org-scoped)
```

Role-based rendering: admin routes and every write affordance (buttons, switches, menu items) are hidden — not disabled — for users lacking the role; the server still enforces.

### 13.5 Screen-by-screen specification

**Login** (`/login`): centered card `max-w-sm` on the page background: product mark, "Sign in to Shepherd" (`text-lg font-semibold`), one full-width primary button "Continue with <provider>" → `GET /auth/login`, where the label comes from `/auth/methods`'s `oidc_display_name` (§7.1) rather than being hardcoded. Footer caption names the same provider's groups. Auth-failure query param renders a destructive Alert above the button; `auth_error=oidc_not_configured` says so specifically and points at Admin → Single sign-on.

**Overview** (`/`): row of four stat cards (grid `grid-cols-4 gap-4`; each: `text-xs text-zinc-400` label over `text-2xl font-semibold tabular-nums` value + small context line): Collectors (value = total, context = "n healthy · n stale"), Active pipelines, Clusters, Sync errors (red value when > 0). Below, two half-width cards side by side: **"Needs attention"** — table of collectors with status Failed or zero live instances (columns Cluster, Role, Status, Last seen; empty state: `CheckCircle2` icon, "All quiet", "Every collector is applying its config.") — and **"Recent changes"** — last 10 audit rows (actor, action verb + resource as one sentence, relative time).

**Collectors list**: filter bar (search by cluster; Role Select; Status Select; org implicit). Columns: Cluster, Role (icon+name badge), Instances (live count; amber text when 0), Version (distinct `alloy_version`s, comma-joined), Status (badge = worst status across instances), Last seen, Served hash (mono short). Empty state: `Radar` icon, "No collectors yet", "Collectors appear here automatically when a spoke cluster registers with a valid agent token.", button "View onboarding snippet" → dialog with the §1 YAML in a code block.

**Collector detail**: header block: `text-xl` "`{cluster}` / `{role}`" with role icon, org Badge, status Badge, and assigned-group chips (Badge outline with `Users` icon). Tabs (shadcn Tabs, underline style):
- *Instances*: table — Instance ID (mono, copyable), Name, Version, OS, Status badge, Error (`AlertTriangle` amber icon with Tooltip showing `remote_config_error`), Last seen. Rows with `unregistered_at` set render at 50% opacity with an "Unregistered" badge.
- *Served Config*: meta row (hash copyable, generated-at, "Built from n pipelines" where each pipeline name links to its editor) above a read-only editor (§13.6) filling remaining height.
- *Pipelines*: compact table of currently-matching pipelines: Name (link), Source badge, Enabled switch (org admin only; flipping calls enable/disable and refetches), Revision, Updated.
- *Access* (visible to org admin): current assignments as a list of rows (group name, GUID mono, remove `X` button w/ confirm); "Assign group" opens a Combobox dialog backed by `GET /api/admin/groups/search` (debounced 300ms, min 2 chars, shows name + GUID).

**Pipelines list**: filter bar (search name; Source Select; Enabled Select). Columns: Name, Source badge, Matchers (each matcher as a mono chip, max 3 shown then "+n"), Enabled (switch, org admin), Revision, Updated by/at. Primary action "New pipeline"; secondary ghost "Open wizard" → `/wizards`. Empty state: `Workflow` icon, "No pipelines", "Create a pipeline by hand or start from a wizard.", two buttons.

**Pipeline editor** (`/pipelines/new`, `/pipelines/:id`): full-height two-pane split (left `w-[380px] shrink-0 border-r overflow-y-auto p-6`, right flex-1 editor column).
- Left pane, top→bottom: Name input; **Matcher builder** — vertical list of rows [key Combobox | operator Select (`=`,`!=`,`=~`,`!~`) | value Combobox | remove ghost `X`], "+ Add matcher" ghost button; key/value comboboxes are fed by `GET /api/orgs/{org}/attributes` (§12) and remain free-text-capable; beneath it a live **match preview** card: "Matches **n** collectors" + up to 5 `cluster/role` mono lines + "+n more" (calls preview endpoint, 500ms debounce; n=0 renders the count in amber with caption "This pipeline currently matches nothing."). Then: Enabled switch with caption "Enabling validates against every affected collector."; Source badge; Revision Select (rev list w/ author+time) — selecting an old revision switches the right pane to a **diff view** (CodeMirror merge view, old vs current) with a "Restore this revision" button (creates a new revision). Danger zone card at bottom: Delete (AlertDialog w/ typed confirm).
- Right pane: toolbar row (`h-11 border-b px-3` flex): left = validation status — live-region text: spinner+"Validating…", or `CheckCircle2` emerald "No problems", or `XCircle` red "n problems"; right = ghost "Format" (disabled placeholder, tooltip "Coming soon"), outline "Validate", primary "Save". Below: the editor (§13.6) `flex-1`. Bottom: collapsible **Problems panel** (`max-h-40`, monospace rows `line:col  message`, click scrolls editor to the position).
- Save with stage-3 failure → Dialog "Validation failed on n collectors": Accordion per collector containing its diagnostics; single button "Back to editing".
- Git-sourced pipelines: entire left pane read-only, editor read-only, and a violet banner across the top: `GitBranch` icon + "Managed in Git — {project}/{repo} @ {branch}{path}" with an external link to ADO and a "Last synced 2m ago · ok" caption.
- Readers see the same screen fully read-only with no Save/Delete/switch affordances.

**Wizard gallery**: grid of cards `grid-cols-3`; v1 card: `AppWindow` icon, title "Application observability", description "Scrape logs and metrics from selected namespaces and ship them to your org's destinations.", "Start" button. A disabled ghost card "More wizards coming" (`border-dashed`, no action).

**Wizard stepper** (`/wizards/{kind}`): one generic runner renders whatever `GetWizardSchema` returns for that kind, so steps and fields are backend-defined — the step list below describes the *intended* app-observability flow, not a UI contract (as shipped it is: 1 Scrape targets, 2 Log collection, 3 Destinations, 4 Collector matching, then Review). Left vertical stepper rail (`w-56`): numbered circles (active = indigo filled, done = emerald check, upcoming = zinc outline) + step labels exactly: 1 Scope, 2 Namespaces, 3 Metrics, 4 Logs, 5 Destinations, 6 Review. Steps 3/4 auto-skip (rendered struck-through) when their signal isn't selected. Right: step content card `max-w-2xl` with footer Back (ghost) / Continue (primary; disabled until the step's zod schema passes). Step specifics: 1 — cluster multi-select (Combobox multi with checkboxes, claimed clusters only) + signal RadioGroup (Metrics / Logs / Both); 2 — namespace chips input (Enter adds; invalid RFC1123 chips render red with tooltip) + include/exclude RadioGroup; 3/4 — the option forms from §11.1; 5 — destination Selects filtered by type, each rendering a summary line (url host, tenant) under the control, plus an inline "New destination" ghost button opening the destinations dialog; 6 Review — for each generated pipeline: name, matcher chips, match-preview line, and a read-only editor with live stage-1/2 diagnostics; footer swaps to "Save as disabled" (outline) + "Save & enable" (primary). Success → toast + navigate to `/pipelines`.

**Destinations**: table (Name, Type badge, URL host, Tenant, Secret `namespace/name` mono, Auth mode, Updated). Create/edit Dialog fields per §5 schema with zod; `type` Select drives conditional fields. Empty state: `Send` icon, "No destinations", "Destinations tell wizard pipelines where to ship telemetry."

**Git** (`/git`): Tabs. *Repo links*: table (Target collector `cluster/role`, Repo `project/repo` mono, Branch, Path, Interval, Last sync (relative + status dot: ok emerald / error red with Tooltip = `sync_error`), Last commit short mono; row actions: Sync now, Edit, Delete). Link Dialog: credential Select, project/repo/branch/path inputs, target collector Select, interval input; "Test & link" button calls verify then creates. *Credentials*: table (Name, ADO org URL, Client ID mono, Created; actions: Test, Edit, Delete). Credential Dialog: name, ADO org URL, tenant ID, client ID, client secret (password input, write-only — edit shows "Leave blank to keep current secret").

**Admin — Orgs**: table (Name, Display name, Admin group (name + GUID), Reader group, Clusters n, Pipelines n). Create/edit Dialog uses the Graph group Combobox for both group fields.

**Admin — Clusters**: two stacked cards. **Unclaimed** (amber left-border accent): table (Cluster, First seen, Roles seen as role-icon row, Live instances) + row action "Claim…" → Dialog with org Select. Empty: "No unclaimed clusters." **Claimed**: table (Cluster, Org, Roles, Instances) + "Unclaim" (typed-confirm AlertDialog warning that collectors stop receiving org pipelines).

**Admin — Single sign-on** (`/admin/auth`, app admin only): one form over §7.1b's settings, grouped as Provider / Groups and administrators / Claim mapping. A provider picker seeds claim names, scopes, and the button label from the preset catalogue (`ListOidcProviderPresets`) and shows that provider's "what you must configure in the IdP for the groups claim to arrive" note — the likeliest way to misconfigure this. "Test connection" runs discovery without saving and renders the discovered issuer and endpoints. The client secret field is write-only: blank means "keep the stored one", and the typed value is dropped from component state the moment the save succeeds. When the chart owns the configuration every control is disabled and a banner says why — an admin must still be able to *read* which provider their cluster trusts. An inline warning fires while the app-admin group list is empty, because saving in that state leaves the local-admin account as the only way in.

**Admin — Agent tokens**: table (Name, Token ID mono, Created by/at, Status: Active / Revoked badge; action Revoke w/ confirm). "New token" Dialog → on create, swaps to a success view: amber Alert "Copy this secret now — it won't be shown again", two copy fields (Token ID / Secret, mono), and a ready-made YAML snippet block (`auth: {type: basic, username: <id>, password: <secret>}`) with copy button; Close button only.

**Audit**: filter bar (actor search, action Select, date range Popover+Calendar). Table: Time, Actor, Action (mono verb badge e.g. `pipeline.update`), Resource (type + linked name), Detail (`ChevronRight` expands a row-panel rendering the JSON detail in a mono `<pre>`).

### 13.6 Editor: Alloy syntax highlighting, autocompletion, and diagnostics

All editors are ONE shared component `web/src/editor/AlloyEditor.tsx` (props: `value`, `onChange?`, `readOnly`, `diagnostics`, `height`). Packages: `@codemirror/state`, `@codemirror/view`, `@codemirror/language`, `@codemirror/autocomplete`, `@codemirror/lint`, `@codemirror/search`, `@codemirror/merge` (diff view), `@lezer/highlight`. Theme: build ONE custom `EditorView.theme` matching §13.1 (zinc backgrounds, JetBrains Mono 13px, active-line `zinc-900/60`, selection indigo at 25% opacity, gutter text `zinc-600`) — do not import a stock theme; derive the light variant from the same definition.

**Highlighting** — `web/src/editor/alloyLanguage.ts` via `StreamLanguage` (a full Lezer grammar is NOT required for v1). Token rules: `//` line + `/* */` block comments → `tags.comment`; double-quoted strings with escapes → `tags.string`; numbers incl. floats and duration/size suffixes (`10s`, `5m`, `512MiB`) → `tags.number`; `true|false|null` → `tags.bool`; dotted identifier chains in block-header position (start of statement, followed by optional quoted label then `{`) → `tags.keyword`; identifier before `=` at statement start → `tags.propertyName`; function-style calls (`convert.nonsensitive(`, `env(`, `sys.env(`) → `tags.function(tags.variableName)`; member expressions referencing components (`prometheus.remote_write.dest.receiver`) → `tags.variableName`; operators/braces/brackets → `tags.operator`/`tags.brace`. Enable bracket matching, `foldGutter` on braces, line numbers, `highlightActiveLine`.

**Autocompletion** — schema-driven, offline, bundled. Create `web/src/editor/alloySchema.ts`: a typed map `componentName → { doc, hasLabel, attributes: [{name, type, required, doc, values?}], blocks: [{name, repeatable, attributes: […]}], exports: [name] }` covering AT MINIMUM every component the wizards emit plus the common set: `discovery.kubernetes`, `discovery.relabel` (incl. nested `rule` block with `source_labels`, `regex`, `target_label`, `replacement`, `action` — `action` gets enum values `keep, drop, replace, labelmap, labeldrop, labelkeep, hashmod, lowercase, uppercase`), `prometheus.scrape`, `prometheus.relabel`, `prometheus.remote_write` (nested `endpoint`, `basic_auth`, `oauth2`, `tls_config`, `write_relabel_config`), `prometheus.exporter.self`, `loki.source.kubernetes`, `loki.process` (nested `stage.json`, `stage.labels`, `stage.template`, `stage.label_drop`, `stage.multiline`), `loki.write` (nested `endpoint`), `otelcol.receiver.otlp`, `otelcol.processor.batch`, `otelcol.exporter.otlp`, `otelcol.exporter.otlphttp`, `remote.kubernetes.secret`, `local.file`, `declare`. Completion source logic (a single `CompletionSource` registered on the language):
1. **Top level / inside `declare`**: complete component names; applying one inserts a snippet — `prometheus.scrape "${label}" {\n\t${}\n}` (label placeholder only when `hasLabel`), with required attributes pre-inserted as `name = ${}` lines. The completion `info` panel shows the component `doc`.
2. **Inside a block**: complete that component's attribute names (`= ` appended) and nested block names (snippet with braces); attributes already present in the block are filtered out; required ones sort first (`boost`).
3. **After `=`**: if the attribute has `values`, complete the enum (quoted); bool type completes `true`/`false`; duration type offers `"15s" "30s" "1m" "5m"`; additionally always offer component-export references harvested by scanning the current document for block headers — seeing `discovery.relabel "annotated"` offers `discovery.relabel.annotated.output`, using each component's `exports` list from the schema (scrape → none, relabel → `output`/`rules`, remote_write/write/exporters → `receiver`, secret/file → `data`/`content`).
4. Trigger on typing (`activateOnTyping: true`) and Ctrl/Cmd-Space; never complete inside strings or comments (guard via token type at the cursor).

**Diagnostics** — a `linter(...)` extension whose source is a parent-provided async callback (the editor never calls the network itself): editor pages wire it to `POST …/pipelines/validate` with 800ms idle debounce, mapping server `{line, col, message, stage}` → CodeMirror `Diagnostic` (`severity: "error"`, from/to computed from line/col to end-of-token). Stage-2 diagnostics without positions attach to line 1 and also appear in the Problems panel. `lintGutter()` enabled (red dot on error lines).

**Read-only mode** hides autocompletion and the lint gutter but keeps highlighting, folding, and search (Ctrl/Cmd-F).

### 13.7 Responsiveness & accessibility

Minimum supported width 1280px; below it the sidebar force-collapses to icons and the pipeline editor stacks (metadata pane becomes a collapsible top section). No mobile layouts. All interactive elements keyboard-reachable in DOM order; visible focus ring (`ring-2 ring-indigo-500/60`); every icon-only button gets `aria-label` + Tooltip; status badges always include the status word (never color alone); the validation status line is `aria-live="polite"`; dialogs trap focus (Radix default). Body-text color pairs from §13.1 meet WCAG AA; do not introduce new pairs.

---

## 14. CLI & config

Cobra commands:

```
shepherd serve                 # run migrations check (fail if pending unless --auto-migrate), start server
shepherd migrate up|down|status
shepherd token create --name X | revoke <id> | list      # direct-DB agent token mgmt (bootstrap)
shepherd validate <file.alloy> # run stages 1–2 locally
shepherd version
```

Viper: config file `shepherd.yaml` (path via `--config`), env override prefix `SHEPHERD_` (dots→underscores). Full schema with defaults:

```yaml
server:    { listen: ":8080", base_url: "https://shepherd.example.internal" }
database:  { url: "postgres://...", max_conns: 20 }
oidc:      { issuer: "", client_id: "", client_secret: "", redirect_url: "", scopes: [openid, profile, email, offline_access, GroupMember.Read.All],
             provider: "entra", display_name: "Microsoft",              # preset key (§7.1a); sets the use_graph_groups default
             subject_claim: "oid", email_claim: "email", name_claim: "name", groups_claim: "groups",
             use_graph_groups: true }                                    # default: true iff provider == "entra"
             # issuer: "" does NOT mean "no SSO" — it hands configuration to app admins via the UI (§7.1b).
auth:      { app_admin_group_ids: [], session_ttl: "8h" }
graph:     { tenant_id: "", client_id: "", client_secret: "" }   # app-mode Graph; may reuse oidc app
agent:     { inactive_after: "3h", delete_after: "720h" }
validate:  { alloy_binary: "/usr/local/bin/alloy", stability_level: "experimental", timeout: "10s" }
gitsync:   { tick: "15s", default_poll_interval: "3m" }
security:  { encryption_key: "" }        # base64 32 bytes, REQUIRED
log:       { level: "info", format: "json" }
```

Fail fast on boot with a clear message if any required field is missing.

---

## 15. Testing requirements (Ginkgo v2 + Gomega)

- `internal/merge`: `DescribeTable` suites for matcher parsing/evaluation (every operator, regex anchoring, zero-matcher rule, git-pipeline bypass) and merge determinism (same inputs → identical bytes → identical hash), declare-wrapping name sanitization, collision of same-named components across pipelines.
- `internal/validate`: fixtures of valid/invalid Alloy snippets; stage-2 tests may be tagged `Label("needs-alloy-binary")` and skipped when the binary is absent; stage-3 test proving a bad pipeline cannot be enabled when it would break an existing collector's merge.
- `internal/agentapi`: integration suite (testcontainers Postgres) using a real generated Connect **client** against an `httptest` h2c server: register → getconfig(empty, unclaimed) → claim cluster → create+enable pipeline → getconfig returns content+hash → getconfig with hash returns not_modified → pipeline update flips hash → status FAILED persisted from request.
- `internal/auth`: session middleware, RBAC matrix test (every role × every endpoint class → allow/deny), agent-token constant-time verify, revoked token rejected.
- `internal/gitsync`: mock ADO server (`httptest`) serving the four endpoints; tests for no-change fast path, add/update/delete file, invalid file rolls back whole commit, force sync endpoint.
- `internal/wizard`: golden-file tests — wizard input fixtures → rendered output compared to committed golden `.alloy` files; goldens must themselves pass stage 1.
- `internal/store`: migration up/down cycle on testcontainers Postgres.
- Frontend: Vitest for matcher-builder logic, wizard zod schemas, editor diagnostics mapping.
- End-to-end: the full-stack suite with a real Alloy agent is specified separately in §18 (`make e2e`).
- Make targets: `make test` (full Go suite, testcontainers included — needs Docker), `make e2e` (§18). `test` runs in CI; `e2e` runs in its own workflow (merge queue / dispatch).

## 16. Milestones (implement in this order; each ends green)

1. **Skeleton**: repo layout, cobra+viper, config validation, migrations 0001, sqlc setup, Makefile, dev Dockerfile, health endpoint, `.golangci.yml` (§20) with `make lint` green, `.goreleaser.yaml` + `Dockerfile.goreleaser` (§21) with `make release-snapshot` green, `docs/spec.md` + the three AGENTS.md files (§22).
2. **Agent protocol**: buf codegen, agentapi with token auth, registration/instances, empty-config serving, lifecycle sweeper, integration suite.
3. **Merge + validation**: matcher engine, declare merge, hashing, serve cache, 3-stage gate, `shepherd validate`.
4. **Pipelines API**: CRUD + revisions + enable gate + preview-matches + audit.
5. **AuthN/Z**: OIDC BFF, Graph transitive groups, sessions, RBAC middleware, admin APIs (orgs, claiming, tokens, group search/assignments).
6. **Frontend core**: shell, login, collectors list/detail, pipelines list/editor with Alloy mode + diagnostics.
7. **Destinations + Wizard**: destinations CRUD, wizard schema/render/commit, wizard UI stepper.
8. **ADO GitOps**: credentials (encrypted), ADO client, gitsync reconciler, repo-links UI.
9. **Hardening**: audit UI, overview dashboard, Helm chart per §17 (incl. `make helm-lint` green), README (deployment, spoke onboarding snippet from §1, destination secret convention §11.4), Prometheus `/metrics` for Shepherd itself (`shepherd_getconfig_total{result}`, request latency per RPC, sync results, validation failures — the e2e suite depends on these metric names).
10. **Local E2E**: everything in §18 — testability hooks, mockmsft, compose stack, all seven scenarios green via `make e2e`.

---

## 17. Helm chart (`deploy/helm/shepherd`) — full specification

Chart `apiVersion: v2`, `name: shepherd`; `appVersion` tracks the image tag. Shepherd is stateless — the chart must support `replicas >= 2` out of the box (sessions and serve-cache live in Postgres; the per-replica singleflight is acceptable per §19).

### 17.1 Templates (each toggled/parameterized as noted)

| Template | Requirements |
|---|---|
| `deployment.yaml` | RollingUpdate (maxUnavailable 0, maxSurge 1). Container args `["serve"]`. Config mounted from the ConfigMap at `/etc/shepherd/shepherd.yaml`, passed via `--config`. Secrets injected as env vars via `envFrom.secretRef` (viper env override handles the rest). Probes: liveness `GET /healthz`, readiness `GET /readyz` (readiness must check DB connectivity + pending-migration state). `securityContext`: runAsNonRoot, readOnlyRootFilesystem, drop ALL caps; an `emptyDir` at `/tmp` (needed by the `alloy validate` temp files, §8). Standard passthroughs: `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `topologySpreadConstraints`, `podAnnotations`, `extraEnv`. |
| `configmap.yaml` | Renders `shepherd.yaml` from `.Values.config` (§14 schema) via `toYaml` — the values structure IS the config structure. Secret-bearing fields (`database.url`, `oidc.client_secret`, `graph.client_secret`, `security.encryption_key`) must NOT appear here; they arrive only as env vars. Checksum annotation on the Deployment pod template (`checksum/config`) to roll pods on config change. |
| `secret.yaml` | Only rendered when `existingSecret` is empty AND `.Values.secrets.*` provided (dev convenience). Production path: `existingSecret: <name>` referencing a secret with keys `SHEPHERD_DATABASE_URL`, `SHEPHERD_OIDC_CLIENT_SECRET`, `SHEPHERD_GRAPH_CLIENT_SECRET`, `SHEPHERD_SECURITY_ENCRYPTION_KEY` — designed to be populated by External Secrets Operator. |
| `migrate-job.yaml` | Helm hook Job (`pre-install,pre-upgrade`, `hook-delete-policy: before-hook-creation,hook-succeeded`, hook-weight `-5`) running `["migrate", "up"]` with the same image/env. Toggle `migrations.job.enabled` (default `true`). When disabled, users may set `--auto-migrate` via `extraArgs` instead — document both, default to the Job. |
| `service.yaml` | ClusterIP, port 8080 → `http`. **`appProtocol: kubernetes.io/h2c`** on the port so Envoy/kgateway negotiates HTTP/2 cleartext to the pod — required for agents using the gRPC/Connect+proto path. |
| `httproute.yaml` | Gateway API HTTPRoute (primary ingress mechanism, toggle `route.enabled`): `hostnames`, `parentRefs` (name/namespace/sectionName) from values. Single route for both UI/API and the agent path (`/collector.v1.CollectorService` is just a path prefix on the same server). |
| `ingress.yaml` | Classic Ingress alternative, toggle `ingress.enabled`, mutually exclusive with `route.enabled` in `values.schema.json`. |
| `serviceaccount.yaml`, `hpa.yaml`, `pdb.yaml` | Standard. HPA on CPU (default off). PDB `minAvailable: 1` when replicas > 1. |
| `servicemonitor.yaml` | Toggle `metrics.serviceMonitor.enabled`; scrapes `/metrics` on the http port; configurable labels for Prometheus-operator selector matching. |
| `networkpolicy.yaml` | Toggle, default off: ingress from gateway namespace label selector + egress to Postgres, `login.microsoftonline.com`/`graph.microsoft.com`/`dev.azure.com` (documented as "allow-all egress unless your CNI does FQDN policies"). |

### 17.2 `values.yaml` shape (top level)

```yaml
image: { registry: "", repository: shepherd, tag: "", pullPolicy: IfNotPresent, pullSecrets: [] }
replicas: 2
config: { ... }            # mirrors §14 exactly, minus secret fields
existingSecret: ""
secrets: {}                # dev-only inline secrets
migrations: { job: { enabled: true } }
route: { enabled: true, hostnames: [], parentRefs: [] }
ingress: { enabled: false, className: "", hosts: [], tls: [] }
metrics: { serviceMonitor: { enabled: false, labels: {} } }
resources: {}, nodeSelector: {}, tolerations: [], affinity: {}, priorityClassName: ""
```

Provide `values.schema.json` validating the above (required fields, enum checks, route/ingress mutual exclusion). Provide `ci/default-values.yaml` and `ci/full-values.yaml`; `make helm-lint` runs `helm lint` plus `helm template` against both and fails on any error. Include `NOTES.txt` printing the URL and a ready-to-paste spoke `remoteConfig:` snippet (from §1) with the chart's hostname substituted.

---

## 18. Local end-to-end tests (`e2e/`) — real Alloy against real Shepherd

The e2e suite proves the full loop **locally with no cloud dependencies**: a genuine `grafana/alloy` container polls Shepherd via `remotecfg`, and the suite asserts Alloy actually *applied* the served config. Everything runs under Docker Compose; the Ginkgo suite (build tag `e2e`, its own module-level label `Label("e2e")`) drives it from the host.

### 18.1 Testability hooks (add these to the main app — small, guarded)

1. `graph.base_url` (default `https://graph.microsoft.com`) and `ado.base_url` (default empty = use each credential's `ado_org_url` host) viper keys so both clients can be pointed at the mock.
2. `shepherd token create --name X --secret Y` accepts a fixed secret ONLY when env `SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true`; otherwise the flag errors. This lets compose provision a deterministic agent token before Alloy boots.
3. OIDC discovery/issuer values come entirely from config already — no code change needed to point at a mock issuer.

### 18.2 Compose stack (`e2e/docker-compose.e2e.yaml`)

| Service | Image / notes |
|---|---|
| `postgres` | `postgres:16-alpine`, healthcheck `pg_isready`. |
| `oidc` | `ghcr.io/navikt/mock-oauth2-server:2.1.x` — a mock OIDC provider with full discovery/JWKS and an interactive login form that accepts a JSON claims blob as the "username", letting each test log in with arbitrary `oid`, `email`, and `groups` claims. Shepherd's `oidc.issuer` points here. |
| `mockmsft` | Built from `e2e/mockmsft/` — one small Go server with two route groups: **Graph**: `GET /v1.0/me/transitiveMemberOf/microsoft.graph.group` (returns groups based on a header/token the suite controls; include one paginated response with `@odata.nextLink` to exercise paging) and `GET /v1.0/groups?$filter=...`; **ADO**: the four endpoints from §10 backed by an in-memory fake repo whose files/commits the suite mutates via a `/__fixture` control endpoint. Also serves the mock token endpoint for the SP client-credentials grant. |
| `shepherd-init` | The shepherd image, one-shot: waits for postgres, runs `migrate up`, then `token create --name e2e --secret <fixed>` with the dev env var set. |
| `shepherd` | The locally built image (`make docker-build` first), `depends_on: shepherd-init: service_completed_successfully`. Config via env: mock issuer, mock graph/ado base URLs, `auth.app_admin_group_ids: ["11111111-...-appadmins"]`, encryption key, `gitsync.tick: 2s`. |
| `alloy` | `grafana/alloy:latest` (pin ≥ v1.12 — needed for `remote_config_status` reporting). Command `run /etc/alloy/config.alloy --storage.path=/tmp/alloy --server.http.listen-addr=0.0.0.0:12345 --disable-reporting`. `e2e/alloy/config.alloy` contains ONLY a `remotecfg` block: url `http://shepherd:8080`, basic_auth with the fixed token, `poll_frequency = "10s"` (the enforced minimum), attributes `cluster = "e2e-cluster"`, `role = "metrics"`. |

### 18.3 Suite mechanics

- `make e2e`: `docker compose -f e2e/docker-compose.e2e.yaml up -d --build --wait` → `ginkgo --tags=e2e ./e2e` → `docker compose ... down -v` (teardown in `SynchronizedAfterSuite`; keep the stack up with `E2E_KEEP=1` for debugging).
- Login helper: performs the real BFF flow with a cookie-jar `http.Client` — `GET /auth/login`, follow to the mock's login form, POST the claims JSON (e.g. `{"oid":"u1","email":"admin@e2e","groups":["...appadmins"]}`), follow the callback, return the authenticated client. Because Shepherd calls Graph (mockmsft) for transitive groups, the suite registers each persona's groups in mockmsft first. Personas: `appAdmin`, `orgAdmin` (member of the org's admin group), `reader` (member of an assigned group only), `nobody`.
- All waits use Gomega `Eventually` with 90s timeout / 2s polling (one full Alloy poll cycle plus slack).

### 18.4 Scenarios (each an ordered `Describe`)

1. **Registration & claiming.** After stack-up, `Eventually` the app-admin API shows cluster `e2e-cluster` unclaimed with a live `metrics` collector instance. Claim it into a freshly created org. Assert Alloy is healthy throughout (`GET alloy:12345/-/ready`).
2. **Pipeline lifecycle (the core loop).** As orgAdmin: create a destination (pointing at a dummy secret name — the pipeline for this test uses a trivial self-contained config like `prometheus.exporter.self "e2e" { }` + a `prometheus.scrape` forwarding to `prometheus.remote_write` at a black-hole URL so no secret machinery is needed); create + enable the pipeline with matcher `cluster="e2e-cluster"`, `role="metrics"`. Assert: (a) served-config endpoint shows the declare-wrapped content and a hash; (b) `Eventually` the collector detail reports `remote_config_status: APPLIED` with that hash's config; (c) Alloy's own API confirms remote components exist — `GET alloy:12345/api/v0/web/components` contains component IDs prefixed with the remotecfg module. Then update the pipeline; assert the hash changes and status returns to APPLIED. Then disable it; assert served config returns to header-only and Alloy drops the components.
3. **not_modified efficiency.** Scrape Shepherd's own `/metrics` and assert the `shepherd_getconfig_total{result="not_modified"}` counter increases across two Alloy poll cycles with no config change.
4. **Validation gate.** As orgAdmin, attempt to save a syntactically invalid pipeline → 422 with line/col diagnostics; attempt to enable a pipeline that breaks the merge (duplicate declare label forged via name collision after sanitization, e.g. names `a-b` and `a_b`) → rejected listing the affected collector, and the served hash is unchanged.
5. **GitOps sync.** Create an ADO credential (mock SP) + repo link targeting the collector, seed mockmsft with a valid `.alloy` file → `Eventually` a `source=git` pipeline exists and Alloy applies the enlarged merge. Push an invalid file via `/__fixture` → sync_status becomes `error`, last good config still served (hash unchanged). Fix the file → recovers.
6. **RBAC.** reader can GET collectors/pipelines of the assigned collector but every POST/PUT/DELETE returns 403; `nobody` gets 403/404 on org resources; unauthenticated `/api/*` returns 401; agent endpoint with a wrong token secret returns Connect `unauthenticated`.
7. **Status FAILED propagation.** Serve a config that passes validation but fails at runtime apply is hard to construct reliably — instead, have mockmsft… skip; simulate by asserting the plumbing: temporarily insert (via SQL through a test-only helper) a FAILED status row is NOT needed — instead assert the field round-trips using scenario 2's transitions (UNSET→APPLYING→APPLIED). `// DECISION` comment allowed here if APPLYING is never observed due to timing.

### 18.5 CI note

`make e2e` must be runnable in any Docker-capable CI runner (no privileged mode, no kind). Total wall-clock budget: under 10 minutes. Compose project name fixed (`shepherd-e2e`) so parallel local runs are refused rather than colliding.

---

## 19. Explicit non-goals for v1 (do not build)

Per-org agent tokens; webhook-triggered git sync; OpAMP support; editing git-sourced pipelines in the UI; multi-wizard framework beyond the one wizard (but keep `internal/wizard` pluggable: a registry keyed by `wizard_kind`); Alloy binary version matrix testing; SSO logout (front-channel); horizontal-scale coordination beyond stateless replicas + Postgres (singleflight is per-replica — acceptable).

---

## 20. Lint rules — golangci-lint v2 (exact config)

Use **golangci-lint ≥ v2.1** and commit this `.golangci.yml` VERBATIM. Note the v2 schema: `version: "2"`, explicit `default: none`, formatters split into their own section, and `exclusions` replacing v1's `issues.exclude-rules`. Do not add, remove, or silence linters without a `//nolint:<linter> // reason` inline comment — `nolintlint` enforces that every suppression names the linter and carries a reason.

```yaml
version: "2"

run:
  timeout: 5m
  build-tags:
    - e2e

linters:
  default: none
  enable:
    # correctness
    - errcheck
    - govet
    - staticcheck        # v2: includes the old gosimple + stylecheck
    - unused
    - ineffassign
    - errorlint          # %w wrapping, errors.Is/As misuse
    - copyloopvar
    - unconvert
    - unparam
    - exhaustive         # enum switches: role, RemoteConfigStatuses, source, auth_mode
    - nilerr
    - durationcheck
    - makezero
    # resource safety (critical for this codebase)
    - bodyclose          # every ADO/Graph http response body
    - noctx              # all outbound HTTP must take a context
    - rowserrcheck       # pgx rows.Err()
    - sqlclosecheck
    - contextcheck
    # security
    - gosec
    # style/consistency
    - revive
    - gocritic
    - misspell
    - nolintlint
    - depguard
    - importas

  settings:
    errcheck:
      check-type-assertions: true
      check-blank: true
    exhaustive:
      default-signifies-exhaustive: true
    gosec:
      excludes:
        - G104            # duplicate of errcheck
    gocritic:
      enabled-tags: [diagnostic, performance]
      disabled-checks:
        - hugeParam       # noisy with config structs
    revive:
      rules:
        - name: exported            # all exported identifiers documented
        - name: context-as-argument
        - name: error-return
        - name: error-strings
        - name: early-return
        - name: unexported-return
        - name: var-naming
    depguard:
      rules:
        main:
          deny:
            - pkg: github.com/pkg/errors
              desc: use stdlib errors + fmt.Errorf %w
            - pkg: io/ioutil
              desc: deprecated; use io / os
            - pkg: database/sql
              desc: use pgx via internal/store only
            - pkg: gorm.io
              desc: sqlc only (rule 4 in §0)
    importas:
      alias:
        - pkg: github.com/grafana/alloy-remote-config/api/gen/proto/go/collector/v1
          alias: collectorv1

  exclusions:
    generated: lax                  # skip generated files (sqlc, buf output)
    paths:
      - gen
      - internal/store/sqlc
    rules:
      - path: _test\.go
        linters: [gosec, unparam, noctx, contextcheck]
      - path: internal/testutil/
        linters: [gosec]
      - path: e2e/
        linters: [gosec, noctx]

formatters:
  enable:
    - gofumpt
    - gci
  settings:
    gci:
      sections:
        - standard
        - default
        - prefix(shepherd)          # module path — adjust to the actual go.mod module
```

`make lint` = `golangci-lint run ./...`; `make fmt` = `golangci-lint fmt` (v2 runs the formatters). CI runs `make lint` on every push; a lint failure fails the milestone.

Frontend equivalent (brief, also enforced): ESLint 9 flat config with `typescript-eslint` strict + `eslint-plugin-react-hooks`; Prettier with default options; `npm run lint` and `npm run typecheck` (`tsc --noEmit`) both green.

---

## 21. Release rules — GoReleaser v2 (exact config)

Use **GoReleaser ≥ v2.5**. Releases are tag-driven (`vX.Y.Z`, semver). Commit this `.goreleaser.yaml`; the only permitted local deviations are the registry/owner env vars.

```yaml
version: 2

project_name: shepherd

before:
  hooks:
    - go mod tidy
    - buf generate
    - sqlc generate
    - sh -c "cd web && npm ci && npm run build"   # SPA must exist before go build (go:embed)

builds:
  - id: shepherd
    main: ./cmd/shepherd
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X shepherd/internal/version.Version={{ .Version }}
      - -X shepherd/internal/version.Commit={{ .ShortCommit }}
      - -X shepherd/internal/version.Date={{ .CommitDate }}
    mod_timestamp: "{{ .CommitTimestamp }}"        # reproducible builds

archives:
  - formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

sboms:
  - artifacts: archive        # syft-generated SBOM per archive

dockers:
  - id: amd64
    use: buildx
    goarch: amd64
    dockerfile: deploy/Dockerfile.goreleaser
    image_templates:
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-amd64"
    build_flag_templates:
      - --platform=linux/amd64
      - --label=org.opencontainers.image.version={{ .Version }}
      - --label=org.opencontainers.image.revision={{ .FullCommit }}
      - --label=org.opencontainers.image.source={{ .Env.SOURCE_URL }}
  - id: arm64
    use: buildx
    goarch: arm64
    dockerfile: deploy/Dockerfile.goreleaser
    image_templates:
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-arm64"
    build_flag_templates:
      - --platform=linux/arm64
      - --label=org.opencontainers.image.version={{ .Version }}
      - --label=org.opencontainers.image.revision={{ .FullCommit }}
      - --label=org.opencontainers.image.source={{ .Env.SOURCE_URL }}

docker_manifests:
  - name_template: "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}"
    image_templates:
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-amd64"
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-arm64"
  - name_template: "{{ .Env.IMAGE_REGISTRY }}/shepherd:latest"
    image_templates:
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-amd64"
      - "{{ .Env.IMAGE_REGISTRY }}/shepherd:{{ .Version }}-arm64"
    skip_push: "{{ if .Prerelease }}true{{ end }}"

changelog:
  use: git
  sort: asc
  groups:
    - title: Features
      regexp: '^feat(\(.+\))?:'
      order: 0
    - title: Fixes
      regexp: '^fix(\(.+\))?:'
      order: 1
    - title: Other
      order: 999
  filters:
    exclude: ['^docs:', '^chore:', '^test:', '^ci:']

release:
  disable: "{{ if index .Env \"SKIP_SCM_RELEASE\" }}true{{ end }}"
  # GitHub releases when the mirror is GitHub; on Azure DevOps set SKIP_SCM_RELEASE=1 —
  # the images + manifests are the release artifacts there.

snapshot:
  version_template: "{{ incpatch .Version }}-dev+{{ .ShortCommit }}"
```

Supporting rules:

- **`deploy/Dockerfile.goreleaser`** (separate from the dev Dockerfile): does NOT compile Go — it copies the GoReleaser-built binary. Two stages: `FROM grafana/alloy:<pinned> AS alloy` (the exact fleet version, from `deploy/versions.env`) to source `/bin/alloy`, then `FROM gcr.io/distroless/base-nossl-debian12:nonroot` — **base, not static**: alloy is dynamically linked and distroless/static carries no dynamic loader, so on `static` the binary lands in the image unrunnable and `alloy validate` (Stage 2) dies silently. `nossl` because alloy links glibc only. `make check-alloy-runnable` guards this, `COPY --from=alloy /bin/alloy /usr/local/bin/alloy`, `COPY shepherd /usr/local/bin/shepherd`, `USER nonroot`, `ENTRYPOINT ["/usr/local/bin/shepherd"]`. Because `alloy validate` needs `/tmp`, the chart's emptyDir (§17.1) covers it — no shell, no package manager in the final image.
- **`internal/version`**: tiny package with `Version`, `Commit`, `Date` string vars (defaults `"dev"`), printed by `shepherd version` and exposed as a `shepherd_build_info` gauge metric (labels `version`, `commit`) — mirroring `alloy_build_info` so the fleet manager is monitorable the same way as its fleet.
- **Commit convention**: Conventional Commits (`feat:`, `fix:`, `chore:`, …) — required, since the changelog groups depend on it.
- **Make targets**: `make release-snapshot` = `goreleaser release --snapshot --clean --skip=publish` (must succeed locally and in CI on every milestone from milestone 1 onward — this keeps the embed/codegen hooks honest); `make release` = `goreleaser release --clean`, run only on tags by the pipeline, with `IMAGE_REGISTRY`, `SOURCE_URL`, and registry credentials provided by CI (Azure DevOps: a service connection performing `docker login` before the GoReleaser step).
- **Helm chart versioning**: the chart is NOT released by GoReleaser. Chart `version` is bumped manually per chart change; `appVersion` is set to the app tag. CI packages and pushes the chart as OCI (`helm push` to `{{ IMAGE_REGISTRY }}/charts`) in a separate pipeline step triggered by the same tag.

---

## 22. AGENTS.md — agent instruction files (create exactly these)

The repo ships instruction files for AI coding agents that will maintain it after v1. These are **always-on context** injected into every future agent turn, so they are deliberately terse: they carry only what an agent cannot cheaply discover from the code, and they reference this spec by path instead of duplicating it. Do NOT expand them with content generated from the codebase, restate linter-enforced style, or add narrative — every extra line taxes future agents' instruction budget. Hard ceiling: root file ≤ 60 lines; subtree files ≤ 25.

Create THREE files. Root `AGENTS.md`, verbatim (substitute the real module path):

```markdown
# Shepherd

Self-hosted Grafana Alloy fleet manager. Go 1.26 backend (Connect RPC agent API + chi REST),
React 18/TS/Vite SPA embedded via go:embed, PostgreSQL 16. Spec: docs/spec.md (authoritative).

## Commands
- Build: `make build` (builds web first — required for go:embed)
- Test all: `make test` · single pkg: `ginkgo ./internal/<pkg>` · focused: `ginkgo --focus "name" ./internal/<pkg>`
- Tests (needs Docker for testcontainers): `make test`
- E2E (needs Docker, ~10 min): `make e2e`
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
### Ask first
- New Go or npm dependencies · changes to `proto/` · changes to RBAC semantics in `internal/auth`
- Any change to the served-config content format or hashing (breaks fleet rollout)
### Never do
- Commit secrets or `.env` · log token secrets, client secrets, or session IDs
- Edit generated code (`gen/`, `internal/store/sqlc/`) — regenerate instead
- Weaken the validation gate or serve unvalidated config, even in tests of other features
```

`web/AGENTS.md` (≤ 25 lines): commands (`npm run dev|build|lint|typecheck|test`), the pointers an agent needs — design tokens and screen specs live in `docs/spec.md` §13 and are non-negotiable; the shared editor is `src/editor/AlloyEditor.tsx`; API client types in `src/api/`; state via TanStack Query only (no other stores) — and rules: never introduce new color tokens or component libraries; never call `fetch` outside `src/api/`; autocomplete schema changes go in `src/editor/alloySchema.ts` with a matching test.

`e2e/AGENTS.md` (≤ 25 lines): how to run one scenario (`ginkgo --tags=e2e --focus "GitOps" ./e2e`), `E2E_KEEP=1` to keep the stack for debugging, mockmsft fixture endpoint usage, and the rule that scenarios must stay independent of each other except the documented ordered flow.

Finally: copy this specification into the repo as `docs/spec.md` so the references above resolve, and add a one-line pointer to it from the README.

---

## Amendments from Remediation Prompt v1.1 (2026-08-17)

These amendments supersede conflicting text above.

### §D.1 — §8 Stage 3 (amended)
Stage 3 assembles the merged config for every affected collector — affected = matched by the new matcher set ∪ matched by the previous enabled revision's matchers — deduplicates identical merged contents by hash, and runs Stage 1 AND Stage 2 on each unique content (concurrency 4, budget `validate.stage3_timeout` default 30s, fail closed on timeout).

### §D.2 — §7.1 (amended)
The OIDC code flow MUST use PKCE (S256). All auth/session cookies set `Secure: true`; config `auth.insecure_cookies: true` (default false) may disable this for non-TLS local dev only. CSRF: require `X-Requested-With: XMLHttpRequest` on mutating requests.

### §D.3 — §12 (amended)
Add under pipelines: `GET /api/orgs/{org}/pipelines/{id}/revisions` [reader] and `GET /api/orgs/{org}/pipelines/{id}/revisions/{rev}` [reader].

### §D.4 — §13.6 (amended)
There is no machine-readable upstream Alloy schema; `alloySchema.ts` is hand-curated from each component's page under `grafana.com/docs/alloy/latest/reference/components/`. A Vitest drift test asserts every §13.6-listed component is present.

### §D.5 — §17.2 (amended)
Values shape gains `networkPolicy: { enabled: false }`. `values.schema.json` enforces route/ingress mutual exclusion.

### §D.6 — §18.2 (amended)
Alloy service: image pinned to an explicit tag ≥ v1.12.2 (never `latest`).

### §D.7 — §14 config schema (amended)
Config gains `agent.sweep_interval: "5m"`, `validate.stage3_timeout: "30s"`, `auth.insecure_cookies: false`.

### §D.8 — §16 milestone rule (amended)
A requirement marked with a Named test in any remediation prompt is complete only when that test exists, is green, and is listed by name in the review.

### §D.9 — §4.1 e2e review note (amended)
APPLYING is timing-dependent and MAY not be observed; assertions require UNSET→APPLIED at minimum.

## Amendments from Remediation Prompt v1.3 (2026-08-17)

These amendments supersede conflicting text above.

### §D.1 v1.3 — §0.6 gate list (amended)
`make smoke` (container smoke test, < 60s, Docker required) added to the every-milestone gate list alongside `make test`, `make lint`, and `make e2e`. Defined: build production image, migrate up, serve → healthz/readyz 200, SIGTERM clean shutdown, invalid SHEPHERD_LOG_LEVEL fails fast.

### §D.2 v1.3 — §14 log level/format CLI flags (amended)
`--log-level` and `--log-format` persistent flags added to the root cobra command, viper-bound (`log.level`, `log.format`). Precedence: flag > env (`SHEPHERD_LOG_LEVEL`, `SHEPHERD_LOG_FORMAT`) > config file > default (info/json). Unknown level or format fails boot with a clear error naming valid values.

### §D.3 v1.3 — §20 golangci config sloglint (amended)
`sloglint` enabled with `no-global: "all"` and `key-naming-case: snake`. The nolint budget (≤20 total directives) is unchanged. Bare `slog.*` calls in package code (instead of injected logger) are lint errors.

### §D.4 v1.3 — §15 / frontend-testing assertion-depth rule (amended)
No visibility-only tests: a test whose strongest assertion is `locator('body')` or heading-only visibility is not valid (convention — reviewed, not machine-enforced). Debounce tests must assert call-count suppression via `api.calls`, not only eventual firing. Avoid `time.Sleep` in `e2e/` specs — use `Eventually`; bounded backoff inside helper retry loops is the one exemption.

### §D.5 v1.3 — §18 e2e structure (amended)
Single `Ordered` e2e flow with focus-skip guards is the blessed structure. `time.Sleep` grep-banned in `e2e/`; `E2E_KEEP=1` implemented (`make e2e E2E_KEEP=1` leaves stack running). The `/api/v0/web/components` assertion is RETIRED — APPLIED status + served-hash + Alloy log evidence is the standard proof for scenario 2.

### §D.6 v1.3 — §6.3 recompute contract (amended)
Lazy-in-GetConfig is authoritative: singleflight per collector, `UpsertServeCacheConditional` only (never plain upsert), fail-soft to previous content on recompute error, `shepherd_serve_recompute_failures_total` incremented on failure. Handler-side prewarm (`recomputeOrgCaches`) is optional: same code path, conditional upsert only, panic-safe (recovered + logged), caller-invisible.

### §D.7 v1.3 — Process: review-authored fixes (amended)
Reviews may apply minimum fixes needed to execute the suite under review; every fix must be logged. All review-authored fixes enter the next remediation prompt as adopt-and-prove items.

### §D.8 v1.3 — §7 / README: token lifecycle logging (amended)
Token lifecycle Info-logging must never include secret material. Token secret is shown once on create via sanctioned `fmt.Printf`; all other token events log only `id` and `actor` at Info level.

## Amendments from Local Admin Implementation LA-1 (2026-08-17)

### §7.2 Local Admin (optional break-glass account)

**Purpose:** Single local admin account for local development (no Entra) and emergency break-glass access when OIDC is unavailable.

**Config block:**
```yaml
auth:
  local_admin:
    enabled: false            # default
    allow_with_oidc: false    # must be true to allow alongside OIDC
    username: "admin"         # default
    password_hash: ""         # argon2id encoded string — env/Secret only, NEVER ConfigMap
    session_ttl: "1h"         # default; must be >= 5m
```

**Double opt-in rule:** `enabled=true` + `oidc.issuer != ""` + `allow_with_oidc=false` → **boot failure**. Both keys must be explicitly set.

**Single account scope:** Only one local admin account is supported. Permanent decision.

**`user_oid` convention:** Local sessions store `user_oid = "local:" + username`. This is permanent; no rename will occur.

**Password hashing:** Argon2id via `shepherd hash-password` (interactive no-echo, or `--password-stdin` for scripting). Params: time=1, memory=64MiB, threads=4, salt=16B, key=32B, standard `$argon2id$v=19$...` encoding.

**Endpoints:**
- `GET /auth/methods` — always registered (no auth, no CSRF), returns `{"oidc":bool,"local_admin":bool}`, `Cache-Control: max-age=60`.
- `POST /api/auth/local/login` — registered ONLY when `local_admin.enabled` (404 when disabled). Passes through CSRFMiddleware. Constant-shaped failure response on wrong username or password. Success: session with `source="local"`, `is_app_admin=true`, `id_token_expires=NULL`, TTL from `session_ttl`.

**Non-dismissible break-glass banner:** When `me.auth_method === "local"`, amber banner renders between the nav and content on every page, every load. Cannot be dismissed.

**Actor wiring:** `SessionMiddleware` sets the actor context for ALL sessions: OIDC → `sess.Email`, local → `sess.UserOID` (`"local:admin"`). Audit rows are no longer anonymous.

**`/api/me` response:** Gains `auth_method: "oidc"|"local"` field.

**Security:** No rate limiter in v1 — argon2id cost + `subtle.ConstantTimeCompare` + constant-shaped failure response. A `// FUTURE:` comment marks the per-IP rate limiting location.

**Helm:** `auth.local_admin` block fully commented out in values.yaml with break-glass warning header. `SHEPHERD_AUTH_LOCAL_ADMIN_PASSWORD_HASH` documented as a Secret key, never a ConfigMap value.

### §D.9 v1.3 — Sessions schema (amended)
Sessions table gains `source text NOT NULL DEFAULT 'oidc'`. `id_token_expires` is now nullable (local sessions have no ID token). Migration 0003. Both `GetSessionByID` and `CreateSession` include the `source` column.

## Amendments from FS-1 Full-Stack Integration Tests + Local Dev Stack (2026-08-17)

### §12 (amended) — /api/me canonical contract

**Unauthenticated:** `401 {"error":{"code":"unauthenticated","message":"not authenticated"}}`

**Authenticated:**
```json
{
  "user_oid": "string",
  "email": "string",
  "display_name": "string",
  "is_app_admin": boolean,
  "auth_method": "oidc"|"local"|"dev",
  "orgs": [{ "id": "uuid", "name": "string", "display_name": "string", "role": "admin"|"reader" }]
}
```

`orgs` lists orgs the session user has access to. For `is_app_admin=true`: all orgs with `role="admin"`.
For non-app-admin: orgs where session `group_ids` match `admin_group_id` (role="admin") or `reader_group_id` (role="reader"). Always an array, never null.

### §12 (amended) — GET /api/admin/clusters canonical contract

Returns ALL clusters (claimed + unclaimed). Shape: `{id, name, org_id (empty UUID if unclaimed), created_at}`.
Use `?unclaimed=true` to filter to unclaimed only. The old behaviour of returning only unclaimed clusters by default is a bug (fixed in FS-1).

### §14 (amended) — Dev tooling

`shepherd dev seed` and `shepherd dev create-session` are developer-only CLI commands:
- Direct DB access — no HTTP flow, no auth required.
- `dev seed`: idempotent, ON CONFLICT DO NOTHING, creates 2 orgs/clusters/collectors/pipelines/token.
- `dev create-session`: inserts session row with `source='dev'`, prints session ID to stdout.
- Both commands must NEVER be exposed via HTTP and are not to be called in production.
- `shepherd dev create-session --persona` supports: `appadmin`, `orgadmin-platform`, `reader-platform`, `nobody`.

### §15 (amended) — Test suite layers + CI ordering

Three Playwright layers:
1. **Mocked suite** (`make test-ui`, `playwright.config.ts`): full network mock at API boundary. Scope: UI behaviour, component contracts, loading/error states, mock-state flows. No real backend.
2. **Fullstack suite** (`make test-fullstack`, `playwright.fullstack.config.ts`): REAL backend at `:8080`, NO `page.route()` interception. Scope: UI-API contract correctness, RBAC enforcement, real CRUD persistence, real recompute path. `workers: 1`, fresh DB per suite.
3. **E2E suite** (`make e2e`): Alloy agent protocol (remotecfg polling, GetConfig RPC, hash/not-modified). Merge queue only.

**CI ordering:**
```
[lint ∥ build ∥ guards ∥ generated-drift ∥ test ∥ web ∥ test-ui ∥ test-fullstack] → (merge queue) e2e
```

One documented exception: `.github/workflows/e2e.yml`'s `e2e-egress` job (`make e2e-sim`) also
runs on any PR touching the S3 sandbox containment surface. Its first ginkgo pass is the egress
probes under `--fail-on-empty`: those probes are the ONLY control that bounds what a sandbox run
can reach — the transform bounds credentials, not reachability — so reviewing a change to the sim
network, the simulator or the transform without having exercised them is reviewing the claim
rather than the control. Its second pass runs the S3 run-lifecycle specs, so the same job also
proves the sandbox DELIVERS (a run reaching `completed` with captured series) and not only that it
contains. See VB-1 §6.4, `docs/proofs/simulator-containment.md` §P0 and
`docs/proofs/sandbox-sim-e2e.md`.

`test-fullstack` runs on every PR, parallel with `test-ui`. Path-filter: skip for docs-only changes. Compose logs artifact on failure to `/tmp/fullstack-stack.log`.

### §15 (amended) — Fullstack "no mock" rule

The fullstack suite (`web/tests/fullstack/`) must NEVER use `page.route()`. CI gate: `grep -rn "page.route\|page\.route" web/tests/fullstack/` → zero.

### §7 (amended) — Dev stack

`make dev` boots the local development stack at `:8080` with seeded data and local-admin login (`admin` / `admin`; the e2e stack uses `admin` / `e2e-local-admin-pass`). The dev stack is defined in `dev/docker-compose.dev.yaml` and `dev/shepherd.dev.env`. All dev secrets are prefixed `dev-only-`. The dev compose is also the fullstack test stack — it cannot drift silently because `test-fullstack` in CI boots it on every PR.

---

## Amendment — git provider generalisation (2026-08-19)

Supersedes §10 and the ADO-specific parts of §5 (`ado_credentials`, `repo_links.project`),
§12 (`/ado-credentials/*`), and §18.4 scenario 5.

**Requirement.** GitOps targets **any standard git server**, over HTTPS or SSH, as broadly
as possible. Provider-specific work is confined to **authentication**: Azure DevOps needs an
Entra **service principal**, GitHub needs a **GitHub App**; every other host is served by
ordinary git credentials (PAT / basic / SSH deploy key / anonymous).

**Consequences.**
- Transport is the git wire protocol (`go-git`), not a provider REST API. Change detection
  is `ls-remote`; fetch is a shallow single-branch clone.
- `ado_credentials` becomes `git_credentials` with a `kind` discriminator
  (`none` | `basic` | `pat` | `ssh` | `ado_sp` | `github_app`) plus a `provider_config` JSONB
  for kind-specific non-secret fields; `repo_links` carries a `repo_url` clone URL in place of
  `project` + `repository`.
- `internal/ado` is reduced to the Entra client-credentials token provider; `github_app`
  mints installation tokens from an RS256 JWT and supports GitHub Enterprise Server.
- Per-credential private-CA trust and a default-off insecure-skip-verify escape hatch; SSH
  host keys verified against a per-credential `known_hosts` with no accept-any mode;
  configurable clone size/file/timeout limits.
- Tests run against a real **Gitea** container; `e2e/mockmsft` keeps only its Entra/Graph
  role. E2E scenario 5 pushes real commits, including the change-an-existing-file case.

Full design, migration plan, and open decisions: `docs/git-provider-design.md`.

---

## Amendment — docs truth pass (2026-08-21)

Corrections applied in place above, recorded here so the history is legible:

- **Dev stack password** (§7 amended). The dev stack's local-admin login is `admin` / `admin`
  (source of truth: `dev/shepherd.dev.env`, verified by `test-fullstack` in CI on every PR).
  `e2e-local-admin-pass` is the **e2e** stack's password. The §7 amendment above previously
  conflated the two.
- **Migrations path** (§3). Migrations live at `internal/migrations/sql/`; there is no root
  `migrations/` directory. The layout listing is corrected.
- **ESLint → Biome** (supersedes §20's frontend half). The frontend uses **Biome**
  (`web/biome.json`) for linting and formatting; ESLint 9 + Prettier were never adopted.
  `web/AGENTS.md` is authoritative for frontend tooling.
- **CI-gate reality** (amends §D.4 v1.3 and the §15 fullstack "no mock" rule). The
  `page.route` ban in `web/tests/fullstack/` is now enforced for real: the Makefile's
  `check-no-route-mocks` guard greps for it, and CI's guards job runs it on every PR. The
  `locator('body')` and
  `waitForTimeout` greps described above as CI gates were **never built** — those claims are
  struck. `waitForTimeout` is a convention, not a machine-enforced ban: canvas/drag
  interaction helpers in the visual-builder specs legitimately use real waits where
  `page.clock` cannot drive a mouse-drag simulation. Debounce and loading-state specs still
  must use `page.clock` and call-count assertions.
- **`make test-integration` removed** (amends §15's CI ordering). The `-tags=integration`
  build tag matched zero files, so the target ran the identical test set as `make test` — the
  unit/integration split never existed. `make test` runs all Go tests and requires Docker
  (testcontainers Postgres, docker-shimmed `alloy validate`). CI now runs
  lint/build/guards/generated-drift/test/web/test-ui/test-fullstack in parallel on every PR;
  e2e-egress paths-filtered and e2e on the merge queue (`e2e.yml`); e2e-k8s nightly; schema-verify
  weekly. `make smoke` remains unwired (tracked follow-up in `ci.yml`'s header).
