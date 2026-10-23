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
7. Go 1.27+, formatted and lint-clean per the **exact** golangci-lint v2 config in §20 (`make lint` green at every milestone). Frontend: TypeScript strict mode, no `any`.
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

**Inventory labels** are a separate string-to-string map on the logical collector, capped at 64 entries. App and org admins can add, update, or remove individual keys; viewers can view them. Keys are normalized to lowercase and contain 1-128 ASCII bytes using letters, digits, `.`, `_`, `-`, or `/`; values contain 1-512 bytes without control or Unicode format characters. PostgreSQL also limits the stored map to 64 KiB. Labels survive agent polls and instance replacement. They support inventory search and grouping only: they do not modify Alloy-reported attributes, cluster ownership, pipeline matching, or served configuration. Collector details display every instance's reported attributes separately from editable labels. Inventory summaries omit reported attributes.

**Role is a signal contract, not just a label.** Each role in `metrics | logs | singleton | receiver` has a fixed set of observability signals (metrics/logs/traces/profiles) it is allowed to serve — `role=metrics` only ever gets a pipeline that carries metrics, `role=logs` only logs, `role=receiver` carries metrics/logs/traces (OTLP + Faro ingest) but not profiles, and `role=singleton` is unrestricted (self-monitoring legitimately mixes signal kinds). `internal/signals` is the source of truth: `internal/signals.Derive` reads a pipeline's Alloy syntax against the schema artifact's wire types to compute its signal set, and `internal/signals.Policies`/`Enforce` hold the role → allowed-signals table. `internal/merge.WithRoleEnforcement` applies it at config-assembly time — a pipeline whose signals the target role's policy does not allow is excluded from that collector's assembled config rather than served silently mismatched (docs/gateway-tier-plan.md W1; excludes are recorded in `AssembleResult.Exclusions` and the generated config's header comment). Enforcement covers both `internal/mgmtapi`'s write-time paths (validate/preview/recompute-on-save) and `internal/agentapi`'s lazy recompute — the path a real collector's `GetConfig` poll takes when `serve_cache` is dirty. The gap that once existed between the two (`internal/agentapi.Service` assembling without `WithRoleEnforcement`) was closed 2026-08-22; see `docs/project-status.md` F-SIGNAL-SERVE (closed).

Spoke clusters already run local chart-generated config (clusterMetrics, podLogs, etc.). Alloy runs remote config in an **isolated component controller** — remote pipelines cannot reference local components. Consequence: **every remote pipeline must be self-contained**, including its own destination components (`prometheus.remote_write`, `loki.write`, `otelcol.exporter.*`). Credentials inside remote pipelines are never inlined; they use the `remote.kubernetes.secret` component to read secrets that already exist on the spoke cluster (§11.4).

---

## 2. Technology stack (fixed — do not deviate)

| Concern | Choice |
|---|---|
| Language | Go 1.27+ |
| CLI | `github.com/spf13/cobra` |
| Config | `github.com/spf13/viper` (file + env, prefix `SHEPHERD_`) |
| Agent API | `connectrpc.com/connect` (Connect protocol, h2c via `golang.org/x/net/http2/h2c`) |
| Management API | Connect `shepherd.mgmt.v1` services (`internal/mgmtapi`), the one contract for Go and TypeScript; `github.com/go-chi/chi/v5` routes the few out-of-contract endpoints (§12) |
| DB | PostgreSQL 16 |
| DB access | `github.com/jackc/pgx/v5` pool + `sqlc` generated queries |
| Migrations | `github.com/golang-migrate/migrate/v4` (embedded, run via CLI subcommand) |
| OIDC | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| Alloy syntax validation | `github.com/grafana/alloy/syntax` + bundled `alloy` binary (`alloy validate`) |
| Proto codegen | `buf` v2, plugins `protocolbuffers/go` + `connectrpc/go` |
| Backend tests | Ginkgo v2 + Gomega; `testcontainers-go` (Postgres module) for integration |
| Frontend | React 19 + TypeScript + Vite |
| Frontend routing/data | TanStack Router + TanStack Query |
| UI kit | hand-rolled primitives in `web/src/components/ui/` (Modal, DataTable, Field, Banner, Section) + Tailwind CSS — no shadcn, Radix or class-variance-authority |
| Code editor | CodeMirror 6 with a custom Alloy language mode (§12.6) |
| Forms | plain React state with the `Field`/`Input` primitives — no react-hook-form, no zod |
| Frontend tests | Vitest + React Testing Library |
| Frontend serving | Built SPA embedded in the Go binary via `go:embed` |
| Container | Distroless-style multi-stage Dockerfile; MUST also copy the `grafana/alloy` binary into the image at `/usr/local/bin/alloy` (needed for validation) |

---

## 3. Repository layout

```
shepherd/
├── cmd/
│   ├── shepherd/main.go            # cobra root — the fleet manager itself
│   ├── shepherd-mcp/main.go        # MCP agent-interface server (stdio), §22 gateway-tier W11
│   └── shepherd-simulator/main.go  # S3 sandbox-run service (VB-1 §6.4)
├── internal/
│   ├── cli/                        # cobra commands: serve, migrate, validate, token, version, healthcheck, dev
│   ├── config/                     # viper loading + typed Config struct + validation
│   ├── server/                     # http server assembly, middleware, embedded SPA
│   ├── agentapi/                   # collector.v1 Connect service implementation
│   ├── mgmtapi/                    # shepherd.mgmt.v1 Connect services + the /api schema routes
│   ├── auth/                       # OIDC flow, local users, sessions, RBAC middleware
│   ├── graph/                      # Microsoft Graph client (user groups, group search)
│   ├── ado/                        # Entra service-principal token provider (ADO auth kind only; see the git-provider amendment)
│   ├── gitrepo/                    # go-git transport: clone/ls-remote, SSH/HTTPS auth kinds, host-key verification
│   ├── gitsync/                    # repo-link polling reconciler
│   ├── store/                      # sqlc output + thin repository wrappers
│   ├── merge/                      # pipeline matching + declare-wrap merge + hashing
│   ├── serve/                      # ComputeServed: the merge→append-baseline→hash→Stage-1 pipeline shared by agentapi's lazy recompute and mgmtapi's eager one
│   ├── validate/                   # 3-stage validation gate
│   ├── signals/                    # derives a pipeline's signal set (metrics/logs/traces/profiles) and the role→allowed-signals policy table
│   ├── reconcile/                  # declared vs served vs observed three-way reconciliation (gateway-tier W6)
│   ├── beacon/                     # ingest for the beacon's projected component inventory (gateway-tier W5)
│   ├── gateway/                    # Gateway API HTTPRoute renderer + tenant-route apply/verify (gateway-tier W3/W4)
│   ├── grafana/                    # optional Grafana service-account client, outcome verification (gateway-tier D7)
│   ├── chartvalues/                # k8s-monitoring Helm values-layer generator (gateway-tier W9)
│   ├── receiver/                   # receiver-tier Alloy pipeline renderer (role=receiver, gateway-tier W4)
│   ├── onboarding/                 # "connect an app" artifacts — endpoint, env vars, IaC/SDK snippets (gateway-tier W7)
│   ├── netshape/                   # recognises host-name shapes a string literal could steer a connection to (sandbox containment)
│   ├── simulate/                   # S3 sandbox run client + worker (DB-free; internal/simulate/worker holds RunWorker)
│   ├── simsvc/                     # shepherd-simulator's own service: capture harness, synthetic sources, endpoint containment guard
│   ├── visual/                     # visual-builder graph model, Go/TS parity renderer, corpus (VB-1)
│   ├── schema/                     # extracted Alloy component schema artifact + embed
│   ├── mcp/                        # MCP agent-interface backend (read-plus-propose slice of shepherd.mgmt.v1)
│   ├── wizard/                     # wizard registry + six wizard kinds' template rendering
│   ├── crypto/                     # AES-GCM secret encryption helpers
│   ├── metrics/                    # Prometheus metrics registration for Shepherd itself
│   ├── telemetry/                  # Connect interceptor / instrumentation plumbing
│   ├── spa/                        # embeds and serves the compiled React SPA
│   └── testutil/                   # testcontainers Postgres harness, fixtures
├── proto/collector/v1/collector.proto   # vendored (§4)
├── gen/                            # buf output (committed)
├── internal/migrations/sql/        # 0001_init.up.sql / .down.sql, ... 0018 (§5)
├── sqlc.yaml
├── buf.yaml / buf.gen.yaml
├── web/                            # React app (Vite root)
│   └── src/{routes,components,lib,api,editor,visual,wizard}
├── deploy/
│   ├── Dockerfile.local
│   └── helm/shepherd/              # full chart, see §17
├── e2e/                            # local end-to-end suite, see §18
│   ├── docker-compose.e2e.yaml
│   ├── mockmsft/                   # small Go server mocking Graph + ADO REST endpoints
│   ├── alloy/config.alloy          # real Alloy agent config used in e2e
│   ├── k8s/                        # kind-based Kubernetes test environment: NetworkPolicy/containment probes, chart deploy, upgrade, LGTM delivery (docs/kind-test-environment-plan.md)
│   └── e2e_suite_test.go ...       # Ginkgo, build tag `e2e`
├── .golangci.yml                   # golangci-lint v2 config, verbatim from §20
├── .goreleaser.yaml                # GoReleaser v2 config per §21
├── Makefile                        # build, generate, test, lint, dev, helm-lint, e2e, release-snapshot targets
└── README.md
```

Every directory above is real as of this writing (`ls -d internal/*/ cmd/*/`); a package with no design note here earns one only if it stops being self-explanatory from its name and `doc.go`.

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

Mount the generated handler at `POST /collector.v1.CollectorService/{Method}` on the main HTTP server, wrapped in h2c so both HTTP/1.1+JSON and HTTP/2+proto work. Read `local_attributes`, falling back to deprecated `attributes` if `local_attributes` is empty. A request message larger than 8 MiB is refused with `CodeResourceExhausted` before the handler runs (the largest real request, a `GetConfig` echoing the loaded config in `effective_config`, is far smaller). Every RPC refuses an empty instance `id`, or one longer than 256 bytes, with `CodeInvalidArgument` before any database work; `RegisterCollector` and `GetConfig` refuse a `cluster` attribute longer than 256 bytes the same way. Alloy's remotecfg `id` is a UUID by default and otherwise usually a hostname, so real ids fit.

**`RegisterCollector`:**
1. Authenticate via agent token (§7.3) or, when collector OIDC is enabled, an OIDC access token (§7.3b). Reject with `connect.CodeUnauthenticated` on failure.
2. Require attributes `cluster` and `role` (role ∈ `metrics|logs|singleton|receiver`); reject with `CodeInvalidArgument` listing what's missing.
3. Upsert `clusters` row by name (org_id NULL if new — "unclaimed").
4. Upsert logical `collectors` row `(cluster_id, role)`.
5. Resolve the org exactly as `GetConfig` step 3 does (an OIDC caller may claim the cluster here); a refusal is `CodePermissionDenied` and writes no instance row.
6. Upsert the `collector_instances` row keyed by `(collector, wire id)` (instance identity, below): store `name`, full `local_attributes` as JSONB, `last_seen = now()`, `unregistered_at = NULL`, plus parsed `collector.version` and `collector.os` attributes if present.

**`GetConfig`:**
1. Authenticate; upsert the `clusters` and logical `collectors` rows exactly as above (a GetConfig from an unknown instance must self-register; agents may restart-loop without re-registering). These two rows are written before the org check: unclaimed-cluster discovery and OIDC auto-claim need them.
2. Resolve the org the config is served for (`resolveOrg`). An agent-token caller resolves through its token's own binding (§7.3) — the token's org, after its cluster/role allowlists; a token an app admin minted or bound claims an unclaimed cluster to that org on this call, any other token is served only a cluster its org already claimed; an unbound token is refused. An OIDC caller may resolve its own org — from an `agent_identities` binding or the token's asserted org — and claims the cluster the same way. A cluster, role or org a caller may not serve is refused with `CodePermissionDenied`, deliberately *not* the empty config of step 4 (§7.3, §7.3b). Only an OIDC caller with neither a binding nor an asserted org takes its org from the cluster's claim.
3. Only once step 2 passes: read the instance's previous `local_attributes` (for match-drift, §6), upsert the instance row `(collector, wire id)` (`last_seen = now()`, `unregistered_at = NULL` — an instance that polls is registered again), then, if `remote_config_status` is set, persist `status` + `error_message` on it. A refused poll leaves no trace on the instance row: no row, attributes, status or `last_seen`.
4. If the resolved org is null (an unclaimed cluster): return an **empty config** `{content: "", hash: sha256hex(""), not_modified: false}` (or `not_modified: true` if the request hash already equals the empty hash). Never error — an unclaimed collector must keep running its local config undisturbed.
5. Otherwise assemble the served config via the merge engine (§6). The merge engine returns `(content, hash)` from the serve-cache.
6. If `request.hash == hash`: return `{not_modified: true, hash: hash}` with empty content. Else return `{content, hash, not_modified: false}`.

**Instance identity.** An instance row is keyed by `(collector_id, id)` — the wire id is unique per logical collector, not globally (migration 0036). An instance never moves between collectors: a poll naming a different cluster or role (a cluster rename, a role change, or another org reporting the same hostname-like id) writes a new row under that collector, and the old row ages out through `agent.delete_after`. So no caller can take over, squat or hide another collector's instance. A cluster rename under an agent token lands on a new collector that the token claims for its org if an app admin minted or bound it and its cluster allowlist admits the new name (and is refused otherwise); under an OIDC caller with no org of its own it lands on a new unclaimed collector and gets the empty config of step 4. Status writes, the silent-poll rule and the previous-attributes read all address the `(collector, id)` row this poll wrote. Per-collector reads (latest instance, instance list, org-wide local attributes) are unaffected; `shepherd_active_collectors` counts rows, so a moved instance counts under its old collector until that row goes inactive. Likewise the old row keeps feeding its collector's `local_attributes` to org-wide matching (§6, the eager recompute and match previews, which take each collector's most recently seen *registered* instance) until `agent.delete_after` removes it — deliberately: filtering by liveness there would change a collector's served config whenever it goes offline. Unregistering the instance id stops it sooner.

**`UnregisterCollector`:** mark instance rows `unregistered_at = now()`; never delete. The request carries only the wire `id`, which may have a row under several collectors, so the caller is scoped by its principal, in one statement: an agent token, or an OIDC caller with an org of its own (a binding, or an asserted org), marks only the rows whose collector's cluster is claimed to that org and, for a token or a binding, within its cluster/role allowlists (for an asserted org, within the token's `clusters` claim; an asserted org that does not exist marks nothing; an unbound agent token is refused). An OIDC caller with no org of its own marks every row with the id — it can already poll as any collector, so this grants nothing new. The answer is success whether rows were marked, the id is unknown or every row is out of scope, so the call reveals nothing about other orgs' instances.

**Refusals** are counted in `shepherd_agent_refused_total{reason}`: `claim_denied` (a token's or binding's cluster allowlist, or a `clusters` claim, excludes the cluster, a token that may not claim named an unclaimed cluster, the cluster is claimed by another org, or an asserted org does not exist), `role_denied` (a token's or binding's role allowlist excludes the role), `token_unbound` (an agent token with no organisation binding — every collector RPC it makes is refused until an app admin binds it), `beacon_denied` (a bound token's beacon write named a collector outside its org; dropped, still 204), `unregister_denied` (`UnregisterCollector` named an id with rows outside the caller's scope; those rows are left alone and the caller still gets success). The first refusal per (instance id, reason) in five minutes is also logged at Warn with the RPC and the ids involved (instance, collector, cluster, role — never credentials or claim contents); repeats are only counted, so a refused collector retrying every poll does not flood the log.

**Inactive** is derived when status is read, never stored (#237): an instance whose `last_seen` is older than `agent.inactive_after` is presented with status `inactive` (and no error) by the management API and is not counted by `shepherd_active_collectors`; `remote_config_status` keeps the agent's last reported outcome, which is presented again once the instance checks in. A collector's status is its most recently seen instance's, so it reads `inactive` only when every instance is stale. Unset or zero `agent.inactive_after` → nothing is ever inactive.

**Database outage** (#374). A database error that means Postgres could not be reached or could not serve the request — a connection, network or timeout failure, or a "shutting down / not accepting connections / read-only standby" SQLSTATE — is answered `CodeUnavailable` by every RPC, never `CodeInternal` (and never `CodeUnauthenticated` from the gate, §7.3). Other database errors keep their codes. Each replica keeps, in memory and bounded (10 000 entries, 64 MiB), the last config it served per (authenticated principal, `cluster`, `role`) — an agent token's id or an OIDC caller's issuer + subject, never anything the request alone asserts — with its hash, refreshed by every successful `GetConfig` and dropped by any refusal or other non-outage failure for that key. An entry is usable for `agent.outage_cache_ttl` (default 60s, 0–1h, 0 disables) after it was written. When the database is unreachable: `GetConfig` answers from that entry with the same hash semantics as step 6 (not-modified when the hashes match), or `CodeUnavailable` when there is none; `RegisterCollector` succeeds without writing when an entry exists for the caller (Alloy stops its remote-config service when start-up registration fails; `GetConfig` self-registers the instance once the database is back), else `CodeUnavailable`. Nothing is written to the database on this path, and a principal is never served an entry recorded for another principal (for OIDC: any differing issuer, subject, app id, asserted org, clusters, roles or scopes claim) or another collector. The database work runs on a server-owned 15 s deadline detached from the caller's, so only a server-side timeout counts as an outage; a request whose own caller has cancelled or timed out (Connect-Timeout-Ms, grpc-timeout) is answered with that code and never from the caches. That bound (15 s) is also the longest a disconnected caller's request holds a pool connection; it never truncates validation, because this path runs only the in-process Stage 1 (Stage 2/3 run on the management API's write path under `validate.timeout` / `validate.stage3_timeout`). For an OIDC caller the key also names the `agent_identities` binding (org, clusters, roles) — or its absence — as this replica last read it on a database-backed request, remembered for the same TTL; with nothing remembered there is no key. `CreateAgentIdentity` and `DeleteAgentIdentity` forget it on the replica that served the call, so a deleted or moved identity is never answered from memory there; another replica (or one changed with `shepherd agent-identity`) can answer under the old binding for at most `agent.outage_cache_ttl`, and only while the database is unreachable.

**Lifecycle sweeper** (background goroutine, interval from config): hard-delete instance rows older than `agent.delete_after` (no viper default; the chart sets `24h`; unset or zero turns the delete off), and refresh `shepherd_active_collectors`. Logical collectors with zero instances remain (they hold assignments) but display as "no live instances".

### 4.2 Hashing

`hash = hex(sha256(content))` over the exact served string. The agent only echoes this back; consistency matters, algorithm choice is server-side.

---

## 5. Data model (PostgreSQL)

Migration `0001_init` creates (all `id` are `uuid DEFAULT gen_random_uuid()` unless noted; all tables get `created_at`/`updated_at` timestamptz):

```sql
orgs(id, name text UNIQUE, display_name text,
     admin_group_id text,          -- group whose members are Org Admins
     viewer_group_id text NULL)    -- optional org-wide viewer group (reader_group_id until 0037)
-- Migration ledger, 0002-0018 (0001 above is the initial schema):
--   0002 sanitized_name generated column (declare-wrap name collision check)
--   0003 sessions.source + nullable id_token_expires (local sessions)
--   0004 collector_instances(collector_id, last_seen) index
--   0005 'visual' added as a valid pipelines.source
--   0006 git_credentials/repo_links generalized off ado_credentials — any git
--        host, pluggable auth kind (F9, docs/git-provider-design.md)
--   0007 simulate_runs — S3 sandbox run storage (VB-1 §6.4)
--   0008 destination templates + credential-free tenant bindings (gateway-tier W2)
--   0009 tenant_routes — storage above internal/gateway's HTTPRoute renderer (W4)
--   0010 beacon_inventory — the ONLY table the beacon ingest path may write (W5)
--   0011 grafana_connections — optional outcome-verification token (D7)
--   0012 teams + service_accounts — scoped write, capability-scoped machine actors (W10)
--   0013 orgs.tenant_id — app-admin-set only, closes the caller-supplied-tenant gap
--        (moved into org_tenants by 0039)
--   0014 oidc_settings — UI-configurable OIDC, second choice after chart/env (§7.1b)
--   0015 users / org_members — local, non-OIDC identity. See §7.2
--   0016 orgs.editor_group_id text NULL (optional org-wide editor group, OIDC path)
--   0017 team_members — explicit local team membership, extends 0012's model
--   0018 service_accounts.role ('editor'|'admin' tier, orthogonal to capability; §7.3)
--   0037 orgs.reader_group_id renamed viewer_group_id (one role vocabulary; §7.2)
--   0039 org_tenants — an org owns several tenants and tenant prefixes; replaces
--        orgs.tenant_id (each org's tenant became its default exact tenant; §11.4)
--   0040 orgs.strict_tenants boolean NOT NULL DEFAULT false — strict tenant mode, set
--        by an app admin only (§11.4 "Strict tenant mode")

org_tenants(org_id uuid REFERENCES orgs ON DELETE CASCADE,       -- 0039; app admin writes only
           tenant text, kind text CHECK (kind IN ('exact','prefix')),
           is_default boolean DEFAULT false,      -- at most one per org, always 'exact'
           created_by text,
           PRIMARY KEY(org_id, kind, tenant))     -- exact tenants UNIQUE across all orgs

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
           created_by text, revoked_at timestamptz NULL,
           org_id uuid NULL REFERENCES orgs ON DELETE SET NULL,  -- 0038; NULL = unbound (refused)
           clusters jsonb DEFAULT '[]', roles jsonb DEFAULT '[]',  -- allowlists, empty = any
           may_claim boolean DEFAULT false)              -- true only when an app admin minted/bound it

serve_cache(collector_id uuid PRIMARY KEY REFERENCES collectors,
           content text, hash text, computed_at timestamptz,
           dirty boolean DEFAULT true, dirty_seq bigint NOT NULL DEFAULT 0,
           format smallint NOT NULL DEFAULT 0)

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

A pipeline has a list of matcher strings using **Prometheus Alertmanager matcher syntax**: `cluster="prod-eu-1"`, `role!="logs"`, `cluster=~"prod-.*"`, `env!~"dev.*"`. Parse them with `github.com/prometheus/alertmanager/pkg/labels.ParseMatcher`. All matchers on a pipeline are **ANDed**. They evaluate against the label set of the target logical collector: `{cluster: <cluster name>, role: <role>}` plus, when the owning org has opted in independently to each (`allow_label_matching` / `allow_local_attribute_matching`, procoduck/shepherd#139), the collector's admin-set "Manage labels" and its agent-reported `local_attributes` — precedence `{cluster, role} > adminLabels > localAttrs`, reserved keys (`cluster`, `role`, `id`, `os`, `alloy_version`, `collector.*`, `shepherd.*`) dropped from either source (`internal/merge.IsReserved`). **`local_attributes` is the single most-recently-seen live instance's whole attribute blob, not a true per-key union across every live instance** — a deliberate simplification (LABEL-MATCHING-PLAN.md §5, decided 2026-09-18): a collector with more than one genuinely concurrent live instance (an HA pair, or briefly during a rolling upgrade) reporting different keys can see served config flap depending on poll timing. **The two recompute paths (§6.3) read `local_attributes` from different places, deliberately:** the lazy recompute inside `GetConfig` matches on the *current request's* `local_attributes` (`recomputeServeCache`, `internal/agentapi/service.go`), because that poll's own `collector_instances` write may not have committed yet and a re-read would match on the previous heartbeat; the eager recompute from `mgmtapi` has no request to read and uses the latest stored instance row (`localAttrsByOrg`, `internal/mgmtapi/helpers.go`). When an agent's attributes have just changed the two can briefly disagree; they converge on the collector's next poll. A pipeline with zero matchers matches **nothing** (safety default — the UI must always require at least one matcher).

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
          <p.contents, indented one level; its final newline adds no blank line, #308>
        }
        <block_name> "default" { }
content := header comment ("// Generated by Shepherd" — no environment label since #308, collector name + id, list of pipeline names+revisions, role-enforcement exclusions;
                           no generation timestamp — the bytes and hash depend only on the merged config, #213)
         + concatenation of all emitted blocks separated by blank lines
         + D6's baseline pipeline, after exactly one blank line
hash := hex(sha256(content))
```

`declare`-wrapping namespaces each pipeline's components, so two pipelines may both contain `prometheus.remote_write "default"` without collision. Pipelines must therefore be written as **standalone** snippets (docs + wizard templates guarantee this).

The wrapping also gives each pipeline its own Alloy loader, which is what the D6 beacon keys its inventory on (`beacon_inventory.component_name`): `alloy_component_controller_running_components` carries the loader's parent path (`controller_path`, e.g. `/remotecfg`) and its own id (`controller_id`, e.g. `<block_name>.default`); the beacon joins the two and keys the row by the first `pipe_*` segment cut at its `.` — `<block_name>` — so a module nested inside a pipeline (`controller_path="/remotecfg/<block_name>.default"`) counts toward that pipeline, and the pipeline is healthy only if every component under it is. Loaders outside a pipeline (the remotecfg root, where the baseline runs; the local bootstrap config) are not stored. Reconciliation compares these keys with `"pipe_" + SanitizeName(name)` of the served set.

### 6.3 Serve cache & invalidation

`GetConfig` reads from `serve_cache`. A row is recomputed (and re-validated at stage 3, §8) when `dirty = true`, missing, or its `format` is older than the binary's `serve.Format` (migration 0033; a dirty mark resets `format` to 0, so a recompute by a binary that does not set it never reads as current). Mark dirty for all potentially affected collectors whenever: a pipeline is created/updated/deleted/toggled (dirty every collector in the org — cheap and correct), a git sync changes files (dirty that collector), a cluster is claimed/unclaimed, or a matcher-relevant attribute changes on registration. Recomputation happens lazily inside `GetConfig` with a per-collector `singleflight` guard, and eagerly from `mgmtapi` after every write (the lazy path matches on the polling request's own `local_attributes`, the eager path on the last stored ones — §6.1). Both paths read the row's `dirty_seq` **before** loading pipelines and write with a compare-and-swap on it (every mark bumps the generation), so a recompute that raced a newer mark writes nothing and the recompute that observed the newer generation wins — stale content can never clear a newer dirty flag (migration 0020). If recomputation fails validation, KEEP the previous cache entry, log an error, count it in `shepherd_serve_recompute_failures_total`, and record the failure in `audit_log` — on both paths, through one helper (`serve.RecordFailure`); the audit row (action `serve.recompute_failed`, actor `serve`, actor type `system`, resource the collector, detail `dirty_seq`, `path`, `error`) is written once per collector and dirty generation (`serve_cache.last_failed_seq`, migration 0035), not once per poll — never serve a broken config and never serve empty because of a merge bug.

---

## 7. AuthN/AuthZ

### 7.1 OIDC (human users) — standard BFF code flow

- Shepherd is a **confidential client** at a spec-compliant OIDC issuer. Entra ID is the reference deployment, but any provider that serves a discovery document works — see §7.1a for the per-provider knobs and §7.1b for where the configuration comes from.
- Viper config: `oidc.issuer` (Entra: `https://login.microsoftonline.com/<tenant>/v2.0`), `oidc.client_id`, `oidc.client_secret`, `oidc.redirect_url`, `oidc.scopes` (default `openid profile email`; the Entra preset adds `GroupMember.Read.All` only when it falls back to Graph group lookup — `offline_access` is deliberately not requested, see `internal/config/config.go`).
- `GET /auth/login` → generate `state` + PKCE verifier, store in a short-lived httpOnly cookie, redirect to the authorize endpoint.
- `GET /auth/callback` → verify state, exchange code (with PKCE) for tokens, verify ID token with go-oidc. Resolve the user's groups per §7.1a. Store the session row (§5) with `group_ids`, set session cookie: httpOnly, `Secure`, `SameSite=Lax`, name `shepherd_session`. Session TTL: `auth.session_ttl` (default 8h; the row's `expires_at` is fixed at creation and does not extend on activity). An OIDC session additionally ends at the ID token's own expiry (`sessions.id_token_expires`, typically ~1h for Entra/Okta) even if the row's TTL has not elapsed yet: `SessionMiddleware` rejects it and deletes the row on the next request past that point, since there is no refresh-token flow to silently extend it.
- Local sign-in (`POST /api/auth/local/login`, §7.2) is throttled per account and per source IP by an in-process token bucket, to blunt password guessing against the argon2id-hashed local credential store.
- `MeService.GetMe` (§12) → current user profile + computed roles. `GET /auth/logout` → delete session (the SPA's Sign out button calls it with GET).
- `GET /auth/methods` → which sign-in methods the login page should offer, plus the label for the OIDC button. It reports whether OIDC is **live** (a discovered provider is loaded), not merely whether one is configured: a saved-but-undiscoverable provider must not render a button that can only dead-end.
- Every management call (§12) and `/api/schema/*` requires a valid session (middleware). CSRF: require header `X-Requested-With: XMLHttpRequest` on mutating requests (sufficient with SameSite=Lax).

### 7.1a Provider portability — claims and groups

Which claim carries the subject, the email, the display name, and the group list is per-provider configuration, not a constant. `internal/auth.Presets` holds the built-in catalogue (Entra, Okta, Google, Auth0, Keycloak, Cognito, GitLab, authentik, OneLogin, generic); a preset only supplies **defaults** — every value it prefills is stored explicitly, so the effective configuration is readable without consulting the preset table.

- **Subject** — `oidc.subject_claim`, default `oid` (Entra's immutable object ID, what this code has always read). Every other provider needs `sub`. Falls back to the ID token's spec-mandated `sub` when the configured claim is absent, so a mistyped setting degrades to a working identity rather than an empty `user_oid`.
- **Groups** — `oidc.groups_claim`, default `groups` (`cognito:groups` for Cognito, `groups_direct` for GitLab, a namespaced URI for Auth0). Values are matched against `auth.app_admin_group_ids` and against each org's `admin_group_id` / `editor_group_id` / `viewer_group_id`; for a non-Entra provider those columns therefore hold whatever the IdP emits (usually a group name or path), not a GUID. Nothing in the schema ever assumed a GUID — only the Entra-shaped documentation did. The reader accepts all three shapes providers use: a JSON array, a bare string, or a space-separated list.
- **Microsoft Graph** — `oidc.use_graph_groups` (default: true when `oidc.provider` is `entra`, false otherwise) keeps the transitive lookup `GET {graph_base_url}/v1.0/me/transitiveMemberOf/microsoft.graph.group?$select=id,displayName&$top=999` (follow `@odata.nextLink`). It stays the default for Entra because Entra omits the groups claim entirely on >200-group overage. `graph.base_url` is pinned to Microsoft's own Graph hosts (global plus the sovereign clouds) on the UI-managed path: the signing-in user's **delegated access token** is sent to it as a bearer credential, so an unconstrained value would let an app admin redirect every user's token to a collector they control.
  - **Behavioral change, deliberate:** when the Graph call fails, the groups claim is now used as a fallback. Previously a Graph error yielded *no* groups, which silently stripped every administrator of access during a Graph outage or a revoked `GroupMember.Read.All` consent. The claim is signed by the same IdP so it cannot be forged, but it is **not the same set** — claim scope is configured per app registration ("all groups" vs "groups assigned to the application"), and AD-synced groups may emit `sAMAccountName` rather than GUIDs. An Entra deployment that wants the old all-or-nothing behavior must leave the groups claim unmapped in its app registration.

### 7.1b Configuration source — chart, or the UI

OIDC configuration has exactly two sources, and **`oidc.issuer` decides which**:

- **Set in the chart / environment** → chart config wins. The admin UI shows the values read-only and refuses writes (`FailedPrecondition`). A cluster whose identity provider is declared in git must not be silently re-pointed by whoever holds an app-admin session.
- **Not set** → an app admin configures a provider from the UI (Admin → Single sign-on), persisted to the singleton `oidc_settings` row (migration 0014). The client secret is AES-256-GCM-encrypted at rest via `internal/crypto`, following `git_credentials.client_secret_enc`'s precedent, and is never returned by the API — `GetOidcSettings` answers `client_secret_set` only. A local app-admin account is the bootstrap path — the administrator created on first boot, or any local user with app-admin rights (§7.2 as superseded).

Saving takes effect **without a restart**: `auth.Handler` holds the discovered provider and oauth2 client behind an atomic pointer that `Reload` swaps, and `/auth/login` + `/auth/callback` are mounted unconditionally (they redirect to `/login?auth_error=oidc_not_configured` when no provider is live). Other replicas pick the change up within 30s via a staleness check on the auth paths. Discovery runs **before** the write whenever the configuration is being enabled, so a provider that cannot discover is never stored as live — the admin who would have to fix it may be relying on that same login page.

`AdminService` carries the surface, app-admin only, with no org-scoped variant: this configuration decides who can hold an app-admin session in the first place, so delegating any part of it would let an org admin widen their own reach. `TestOidcSettings` probes a candidate configuration without storing it and reports the discovered issuer, endpoints, advertised scopes, and PKCE support — a failed probe comes back in the response body, not as an RPC error, so the form renders the reason inline. It probes **discovery only**: it never exercises the client ID, secret, or redirect URL, so a green result means "this issuer is real", not "sign-in will work".

**Discovery is a deliberately constrained fetch** (`internal/auth/discovery.go`). It is the one place an authenticated user picks a URL the *server* then retrieves, which makes it an SSRF primitive unless it is bounded — and app admin is an application role, not cluster-admin. Four constraints, each closing a distinct hole:

1. A 10s client timeout. `go-oidc` uses `http.DefaultClient`, which has none; `Reload` holds `reloadMu` across the fetch and three *unauthenticated* routes can trigger one, and it also runs before `ListenAndServe`, so an unbounded fetch is both a goroutine pile-up and a boot hang.
2. A `net.Dialer.Control` guard rejecting loopback, private, link-local, CGNAT, and unspecified addresses — applied **after** DNS resolution, so a hostname resolving to `169.254.169.254` and DNS rebinding are both covered.
3. Redirects restricted to `https`, capped at 5 hops.
4. **Discovery errors never carry the response body.** `go-oidc`'s own error is `fmt.Errorf("%s: %s", resp.Status, body)`; surfacing that through `TestOidcSettings` would turn "an admin can probe a URL" into "an admin can read it". Shepherd fetches and parses the document itself and reports status codes only, then builds the provider from `oidc.ProviderConfig` so the constrained client also governs JWKS fetches for the provider's lifetime.

Background refreshes run on a context **detached from the request** (`context.WithoutCancel` + its own timeout): `Reload` spends the 30s backoff before it attempts anything, so inheriting cancellation would let an anonymous client that aborts `/auth/methods` every 30s keep a replica from ever activating OIDC.

### 7.2 Roles

There is one role vocabulary, used everywhere — Go (`auth.Role`), the database
(`org_members.role`, `service_accounts.role`), the API (`shepherd.mgmt.v1.Role`)
and the SPA: **viewer < editor < admin**, each including the one below it. App
admin is not a role but a separate, cross-org flag above every org.

A role is reached by either of two independent paths, and the two coexist —
one is not a fallback for the other:

- **Group-derived** (OIDC): the session's groups claim is matched against
  `auth.app_admin_group_ids` and each org's `admin_group_id` /
  `editor_group_id` / `viewer_group_id`.
- **Locally assigned** (`0015_local_users`): the session names a row in `users`
  (`sessions.user_id`), app-admin comes from `users.is_app_admin`, and the org
  role from `org_members.role`. Shepherd runs fully with no identity provider
  configured at all.

The resulting roles are the same either way:

- **App Admin**: any group in `auth.app_admin_group_ids`, or a local user with
  `is_app_admin`. Can do everything, everywhere: CRUD orgs, claim/unclaim
  clusters into orgs (the only human who can; a collector authenticating
  with an agent token an app admin minted or bound, or with an OIDC identity
  bound to an org, claims its own unclaimed cluster, §7.3, §7.3b), CRUD group
  assignments,
  create/revoke agent tokens for any org and bind or rebind any token, CRUD
  local users, view all.
- **Admin** of org O: member of `O.admin_group_id`, or `org_members.role =
  'admin'` for O. Full CRUD within O: pipelines, destinations, git
  credentials, repo links, wizards, teams, tenant routes, group assignments
  *for O's collectors*, and the agent tokens bound to O (create, list, revoke —
  never an unbound token or another org's, and never a bind). Cannot touch
  other orgs, cannot claim clusters — a token they mint never claims one
  either: it serves only clusters O already claims (§7.3).
- **Editor** of org O: member of `O.editor_group_id`, or
  `org_members.role = 'editor'`. Authors what the org **runs** — pipelines,
  wizards, visual builder, simulations — but cannot change what the org **is**:
  destinations, tenant routes, git credentials, teams and service accounts stay
  admin. The tier exists because "can write a pipeline" and "can re-point
  where telemetry ships" were previously the same permission.
- **Viewer** of org O: member of `O.viewer_group_id` (`reader_group_id` until
  migration 0037), or `org_members.role = 'viewer'`, or holds a higher role.
  Also reached without either: for any collector C, a member of any group in
  `group_assignments` for C is a viewer of C's org, and a member of any team of
  O — local or IdP-group-backed — is a viewer of O, on top of whatever
  `org_members.role` (if any) separately grants. A viewer reads; it writes
  nothing org-wide — no destination, route, wizard or unowned pipeline.

One permission is not a rung of the ladder but a named grant:

- **Team owner-write** (G11, `auth.AuthorizeOwnership` →
  `authorizeTeamOwnerWrite`): a member of a team of O may create a pipeline
  owned by that team, and update, enable, disable, restore a revision of,
  detach from its wizard, delete, or open in the visual builder a pipeline
  that team owns — whatever their org role. Never an unowned pipeline or
  another team's, and never its ownership — `SetPipelineOwner` is admin (§12).
  An editor or admin (or an app admin) writes every pipeline in O through
  their role and never needs the grant.

**Requirement model.** Authorization is one function, `auth.Authorize`, taking
an `auth.Requirement` built from a role rather than a boolean:
`RequireAuthenticated()`, `RequireAppAdmin()`, `RequireOrg(role)` (an app admin,
or at least `role` in the org the request names) and
`RequireAppAdminOrOrg(role)` (the same callers, for a procedure with a
cross-org mode for app admins — `SearchGroups`, and `ListAgentTokens` /
`CreateAgentToken` / `RevokeAgentToken`, where an empty `org_id` is the app
admin's every-token view). Both paths above resolve into
the same rank comparison (`Role.AtLeast`); a value that is not a role — an
unknown stored string, `ROLE_UNSPECIFIED`, a zero `Requirement` — clears
nothing. It is enforced per Connect procedure, not per handler: the interceptor
in `internal/mgmtapi/rpc_interceptor.go` looks up each procedure's requirement
in `procedureRequirements` (§12) and checks it through `auth.Authorize`, failing
closed for a procedure missing from the table. Pipeline writes then add the
per-pipeline `auth.AuthorizeOwnership` check above inside the handler.

On the wire a role is the `Role` enum (`ROLE_VIEWER`, `ROLE_EDITOR`,
`ROLE_ADMIN`; protojson spells them by name). `ROLE_UNSPECIFIED` never grants
anything and is refused with `invalid_argument` wherever a role is written
(`SetOrgMember`, `CreateServiceAccount`).

### 7.3 Agent tokens (machine auth)

Agents authenticate `collector.v1` calls with HTTP Basic auth: username = token UUID, password = 32-byte random secret (base64url). Store only `sha256(secret)`; compare with `crypto/subtle.ConstantTimeCompare`. The secret is displayed exactly once. Implement as a Connect request gate (`connect.WithRequestGate`, `internal/agentapi/auth.go`): the decision happens on the headers, before the body is decompressed or decoded, and a refused call is still counted by `telemetry.RequestGate`.

**Org binding (migration 0038).** Every agent token is bound to an organisation, in the same shape as an OIDC `agent_identities` binding (§7.3b): `agent_tokens.org_id` plus optional `clusters` and `roles` allowlists (jsonb string arrays; empty = any within the org). The gate reads the binding with the token row into one value on the principal (`agentapi.Binding`: org, allowlists, may-claim), and `resolveOrg` resolves a token principal through the same resolver as an OIDC binding (`resolveBoundOrg`): the cluster must be in the cluster allowlist and the collector's role in the role allowlist. A cluster claimed by another org, or a cluster or role outside the allowlists, is `PermissionDenied` (`claim_denied` / `role_denied`) — a token bound to one org can never act as another org's cluster by naming it.

**Claiming.** Claiming a cluster into an org stays an app-admin decision. `agent_tokens.may_claim` is true only for a token an app admin minted (or the operator CLI, which has database access) and for every token `BindAgentToken` binds; such a token claims an unclaimed cluster for its org on a collector's first poll, exactly as an OIDC binding does. A token an org admin or a service account minted has `may_claim = false`: it is served only a cluster its org already claims, its allowlists can only narrow that, and an unclaimed cluster is `PermissionDenied` (`claim_denied`, "an organisation token cannot claim clusters; ask an app admin to claim it") and stays unclaimed — otherwise an org admin could name a new cluster first and pull it, and every OIDC collector on it that resolves by the claim, into their org.

**Unbound tokens are refused.** A token with no org (one created before 0038 — the migration leaves existing rows unbound rather than guessing — or one whose org was deleted, `ON DELETE SET NULL`) still authenticates, but every collector RPC it makes is `PermissionDenied` with `` agent token is not bound to an organisation; bind it with BindAgentToken / `shepherd token bind` `` and counted as `token_unbound`. The cluster and collector rows a poll names are still upserted first, so an unbound token's clusters remain visible to an app admin.

**Who manages tokens.** `CreateAgentToken` (an `org_id` is required, plus optional allowlists), `ListAgentTokens` and `RevokeAgentToken` are `RequireAppAdminOrOrg(admin)` (§7.2): an org admin creates, lists and revokes only the tokens bound to the org the request names — another org's token or an unbound one is invisible to them (`NotFound` on revoke); an app admin calls with an empty `org_id` to see and revoke every token, unbound ones first. `BindAgentToken` sets a live token's org and allowlists in place, and sets `may_claim` — the secret does not change, so collectors keep their credentials — and is app admin only, for a first bind and a rebind alike. An admin-tier service account reaches exactly an org admin's token scope. Every create, revoke and bind is audited against the token's org. The CLI writes the database directly: `shepherd token create --name X --org <slug> [--clusters a,b] [--roles metrics]` (an org is required, except for a dev fixture with `SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true`, which may be created unbound and bound later), `shepherd token bind --id <uuid> --org <slug> [--clusters …] [--roles …]`, `revoke`, `list`.

The beacon remote-write endpoint (`/beacon/write`) authenticates with the same token and keys its inventory and rate limit on the token id. A write from a bound token naming a collector (`shepherd_collector_id`) whose cluster its org does not claim is dropped and counted (`beacon_denied`, logged at Debug, rate-limited), still answered 204; an unbound token's writes are accepted.

**Database outage (#374).** A malformed, unknown, revoked or wrong credential is `CodeUnauthenticated`; a token lookup the database could not answer is `CodeUnavailable` — an outage is never reported as a bad credential. Every request looks the token up in the database (on a server-owned deadline, §4.1), so a revocation or a rebind applies to the next request. Only for an outage, each replica keeps an in-memory, bounded (10 000 entries) cache of verdicts keyed by token id, holding the stored `sha256(secret)` (never the secret) and the token's whole binding (org, allowlists, may-claim) as read with it, for `agent.outage_cache_ttl` after the last database-backed verification. While the database is unreachable, a token whose presented secret matches the cached hash (constant-time compare) gets the identical principal back, binding included; a cached token with no org is `CodePermissionDenied` (unbound tokens are refused, never treated as global); a known token with a different secret is `CodeUnauthenticated`; any other token is `CodeUnavailable`. The binding is part of the last-served key (§4.1), so an entry recorded under an earlier binding is never served under a later one. A token the database reports unknown or revoked is evicted, and `RevokeAgentToken` and `BindAgentToken` evict it on the replica that served the call; a replica that did not see the change (or one changed with the CLI) can act on the old verdict for at most the TTL, and only while the database (where the change lives) is unreachable. The outage path never reaches the org resolver, so it never claims a cluster. An OIDC caller's server-side `agent_identities` binding has the same bound: it is part of the last-served key (§4.1), forgotten on the replica that creates or deletes the identity, and acted on by another replica for at most the TTL during an outage. The beacon answers 503 rather than 401 when its token lookup hits an outage.

### 7.3b Collector OIDC (machine auth for `collector.v1`)

An alternative to the agent token, off by default (v0.8.0; `config.oidc.agent_audience` is the master switch). A collector presents `Authorization: Bearer <access token>` from the same issuer as user SSO — Alloy's `remotecfg` `oauth2` block fetches it natively — and the same request gate verifies it: signature and issuer, `aud` equal to `agent_audience`, and, when configured, the grant gate (`agent_required_role` in `agent_roles_claim`, or `agent_required_scope`). A Bearer header while collector OIDC is off is `Unauthenticated`. The calling client is identified by `(iss, <agent_app_claim>)`, default claim `sub`.

Org resolution (`resolveOrg`, `internal/agentapi/service.go`), first match wins:

1. **Binding** — an `agent_identities` row on `(issuer, app id)` (migration 0021; `shepherd agent-identity create` or Admin → Single sign-on). The cluster must be in the binding's cluster allowlist and the collector's role in its role allowlist; an empty or absent list means any (`resolveBoundOrg`). The cluster is then claimed to the binding's org if unclaimed, or confirmed if it is already that org's.
2. **Asserted org (mode 2, v0.9.0)** — no binding, but the deployment trusts the issuer's org assignment and the token names an org: `agent_trust_org_claim` (a claim holding the org's name) or `agent_org_role_prefix` (the role `shepherd-org:platform-eng` names org `platform-eng`; a token whose roles name more than one org asserts none). Both keys are empty by default. The name is resolved to an org; if the token carries the `agent_clusters_claim` claim, the cluster must be listed in it. This path has no role allowlist — the identity may serve any role. The cluster is claimed exactly as for a binding (`resolveClaimOrg`).
3. **Neither** — OIDC proved liveness only; the org comes from the admin cluster claim, and an unclaimed cluster is served empty (§4.1). This is the one principal with no org of its own (an agent token always has one, §7.3).

A disallowed claim returns `PermissionDenied`, not the empty config §4.1 promises an unclaimed cluster: a cluster or role outside a binding's allowlists, a cluster outside the token's clusters claim, an asserted org that does not exist, or a cluster already claimed by another org — a token can never take over another org's cluster by naming it. This is deliberate. An unclaimed cluster is a normal state in which the collector must keep running its local config; a wrongly bound or wrongly asserted identity is a misconfiguration, and it must fail loudly in the collector's logs rather than quietly serve nothing.

### 7.3a Service accounts (machine callers of the management API)

A separate machine-identity path from agent tokens above: `service_accounts` (migration 0012) are org-scoped credentials for automation that calls `shepherd.mgmt.v1` itself (CI, an MCP agent's on-behalf-of proposals, …), not the agent protocol. A service account is described by two independent axes:

- **Capability** — `propose` or `apply`: whether it may write at all (0012, gateway-tier W10).
- **Role tier** — `editor` or `admin` (migration 0018), the same `Role` a human holds (§7.2): what it may *reach* once capability allows a write. `CreateServiceAccount` requires the tier (`ROLE_EDITOR` or `ROLE_ADMIN`; `ROLE_UNSPECIFIED` and `ROLE_VIEWER` are refused) — `admin` is never assigned implicitly (D3: "admin explicit at creation"), the same way 0015/0016 never implicitly promote a human to admin. A stored tier that is not a role denies every org-scoped procedure; it never falls back to a default.

Before the role column existed, `authorizeServiceAccountProcedure` (`internal/mgmtapi/rpc_interceptor.go`) checked only `sa.OrgID == orgID` for every non-app-admin requirement, so an apply-capability service account reached every admin procedure (`RotateTenantRoute`, `DeleteTeam`, `AddTeamMember`, `DeleteCredential`, `ListAudit`, `ListTeamMembers`, …) it was never explicitly granted. The interceptor now checks the procedure's requirement against the service account's `role` tier with the same `Role.AtLeast` comparison it uses for a human session (§7.2) — **with one absolute floor**: a service account never satisfies an app-admin requirement, regardless of `role`; app-admin procedures are refused to every machine caller unconditionally. An `admin`-tier service account therefore reaches the same org-scoped ceiling an org admin does, never the application-admin surface.

### 7.4 Secret encryption at rest

`internal/crypto`: AES-256-GCM with key from viper `security.encryption_key` (32 bytes, base64; from env/k8s Secret). Random 12-byte nonce prepended to ciphertext. Used for `ado_credentials.client_secret_enc`. Refuse to boot without a valid key.

---

## 8. Validation gate (`internal/validate`) — runs before ANY save or serve

**Stage 1 — Syntax.** Parse with `github.com/grafana/alloy/syntax/parser`. On error, return structured diagnostics `{line, col, message}` (the parser gives positions). This runs on: pipeline save, wizard output, every git-synced file, and the merged output.

**Stage 2 — Semantic (`alloy validate`).** Write the candidate to a temp file and exec the bundled binary: `alloy validate --stability.level=experimental <file>` (stability level from viper `validate.stability_level`, default `experimental` to match the fleet). Timeout 10s. Parse stderr into diagnostics. This catches unknown components, bad attribute types, unset required attributes. Run it on the **declare-wrapped form** of the pipeline (wrap exactly as the merge engine would, §6.2) so what's validated is what ships.

Stage 2 also runs an in-process **port-shape check** (`validate.PortShapes` → `visual.CheckPortShapes`, #233) against the embedded component schema for the pinned Alloy version. `alloy validate` does not evaluate references between components, so it exits 0 on wires Alloy refuses at load. The check refuses exactly two, both certain from the schema and from Alloy's value decoder:
1. A `targets`-typed export (always a `[]discovery.Target`) used as an element of a list literal on a `targets` port: `targets = [discovery.kubernetes.pods.targets]` is a list of lists, which fails with `target::ConvertFrom: conversion from '[]discovery.Target' is not supported` even when discovery finds nothing.
2. A receiver export (a single capsule: `receiver`, otelcol's `input`) assigned bare to a list-cardinality receiver port: `forward_to = prometheus.remote_write.x.receiver` fails with `should be array, got capsule`.

The check flags a reference only when it resolves to a component block declared in scope that is not behind an `import.*` namespace. Anything the schema cannot type precisely produces no diagnostic: function calls such as `array.concat`, `argument.*`, indexing, and cross-signal OTel wiring (`otel.any` is polymorphic). A false positive would block a save or a git sync. A spec runs the check over every Alloy text in the repository and allows no diagnostics except a short list of legacy list-of-lists configs, each of which it asserts *is* flagged. The check needs no binary, so it runs even where `validate.alloy_binary` is empty. Its diagnostics carry `stage: 2` and are returned alongside `alloy validate`'s. Every Stage 2 caller runs it: pipeline save/update/validate, wizard and visual saves, Stage 3, git sync, the sandbox worker and `shepherd validate`. The served-config recompute (`serve.ComputeServed`) runs Stage 1 only, as before.

**Stage 3 — Merge dry-run.** For a pipeline save/toggle: compute the set of logical collectors the new matcher set would affect (plus those affected by the *old* version; for a disable or delete, only the old), assemble each collector's full merged config with the candidate change applied, and run stages 1–2 on each merged result. Affected uses the serve path's own matching (cluster, role, admin labels and `local_attributes` gated by the org's flags, with the same precedence; a repo-sourced pipeline by its link's collector); no other collector's merged config changes, so none other is validated. Merged configs are de-duplicated by content with the collector's identity — the header's `Collector:` comment and the D6 baseline's `shepherd_collector_id` string literal — replaced by a fixed placeholder, so collectors whose merged configs differ only by identity cost one `alloy validate`; the identity is only ever a comment or a string literal, so the placeholder cannot change a verdict, and a failure is reported against every collector that shared the content. If ANY merged config fails, **reject the save** with a response listing the failing collectors and diagnostics. The whole check runs within `validate.stage3_timeout` (default 30s, chart `config.validate.stage3_timeout`) and fails closed past it. This prevents the "one bad pipeline poisons the whole merge" failure mode.

**Tenant lint (strict tenant mode, §11.4).** The lint is **defence in depth**: the boundary is the
backend's own tenant authentication (a Mimir/Loki gateway that ties credentials to tenants). For an
org in strict tenant mode the gate also runs `validate.TenantLint` on the pipeline text as written
(positions in that text, `stage: 1`), parsed with the Alloy syntax parser. Its rule is **refuse
what it cannot verify**: every refusal is an error, and a non-strict org is not linted at all.
Strict mode refuses:

- a tenant the org does not own, and any tenant value it cannot read, in: an `X-Scope-OrgID` key
  (any case) of any headers map — every attribute whose name ends in `headers` (`headers`,
  `http_headers`, …) or is `proxy_connect_header`, each list element; a headers value that is not
  an object literal; `loki.write`'s endpoint `tenant_id`; `loki.rules.kubernetes`' and
  `mimir.rules.kubernetes`' `tenant_id`; `loki.process` `stage.tenant`'s `value` at any nesting;
  Loki's reserved `__tenant_id__` label (it overrides `loki.write`'s `tenant_id` per entry, Alloy
  v1.20.1 `common/loki/client.getTenantID`) in any labels map; a relabel `rule` whose
  `replacement` writes `__tenant_id__`; an `otelcol.auth.headers` header whose key is
  `X-Scope-OrgID`; an `otelcol.auth.bearer` with `header = "X-Scope-OrgID"` and an empty `scheme`
  (its `token` is the tenant);
- tenants taken from the data, which a client can set: `stage.tenant`'s `label`/`source`,
  `stage.labels` setting `__tenant_id__`, a relabel rule writing `__tenant_id__` from a regex
  reference, its default replacement, or `lowercase`/`uppercase`/`hashmod`, a replace rule whose
  `target_label` contains `$` (Alloy expands it), every `labelmap` whose replacement is not a
  literal without `$` that is not `__tenant_id__` (the default `$1` included), and
  `otelcol.auth.headers`' `from_context`/`from_attribute` for the tenant header;
- a **push source that forwards the client's tenant**: `loki.source.api`, `loki.source.heroku`,
  `loki.source.awsfirehose` and `loki.source.gcplog` turn the request's `X-Scope-OrgID` into
  `__tenant_id__` (Alloy v1.20.1), so a `loki.write` would send under the client's tenant. Allowed
  only when, on every path through `forward_to` to every `loki.write`, a `loki.process` or
  `loki.relabel` overwrites or drops the label unconditionally first: a top-level (not in
  `stage.match`) `stage.static_labels` setting it, `stage.tenant` with a `value`,
  `stage.label_drop` listing it or `stage.label_keep` not listing it, or a relabel rule replacing it
  with no `source_labels`/`regex` or a `labeldrop` of exactly `__tenant_id__` (the value written is
  checked where it is set). A path it cannot follow — a `forward_to` that is not a list of
  receivers of components in the same scope, or any component other than `loki.process`,
  `loki.relabel`, `loki.write` and `loki.echo` — is refused;
- anything that may set a tenant and cannot be read: a header key, a relabel `target_label` or
  `action` (matched exactly, as Alloy v1.20.1's `relabel.Action.UnmarshalText` does), or an
  `otelcol.auth.bearer` `header` it cannot resolve; a tenant-header bearer with a non-empty
  `scheme`;
- every `import.file`, `import.git` and `import.http` block (what they load cannot be verified),
  and an `import.string` whose `content` it cannot read or that does not parse. A readable
  `import.string` module is linted with the same rules (up to 8 levels), including the values the
  importing text gives its declares.

A value is *readable* when it is a string literal, a `+` concatenation of readable values, or a
declare argument (`argument.<name>.value`) whose default and every value an instantiation gives it
— read in the declare that instantiation sits in, so a value passed down several declare levels is
followed; declares are keyed by scope, so a nested declare of the same name is a different one —
are readable. A `|`-joined header value must be owned in every part. **The lint is bounded:** a
concatenation may build at most 4 KiB per value and 16 KiB in all, an expression folds to at most
64 values, it follows at most 8 declare hops or nested modules, each (declare, argument) is
resolved once (memoised; one that depends on itself is unreadable), and one lint does at most
50 000 steps. Past any bound a value is unreadable — refused for a strict org. A benchmark, a fuzz
target and specs pin the bounds (the shape that once built a 200 MB string from 4 KB of text
finishes in well under a millisecond).
`prometheus.remote_write` has no tenant label (Mimir reads the header only). Azure AD `tenant_id`
attributes name an identity tenant and are not checked; a spec walks every schema attribute named
like a header or a tenant and fails until each is classified. `Diagnostic.warning` stays on the
wire for diagnostics that do not fail validation, but the tenant lint emits none.

It runs through `ValidatePipeline` (pipeline create/update/restore, the `ValidatePipeline` RPC and
the MCP tools built on it, wizard render/commit, destination re-renders, `rerender-destinations`,
git sync's per-file check, where one refused file refuses the commit), the visual builder's
Validate, and Stage 3 for every candidate; every pipeline write re-runs it in its writing
transaction under the shared ownership lock. **What it still cannot see:** a tenant added by a
proxy or gateway in front of the backend, a backend that takes the tenant another way (basic-auth
username, URL path), and anything a collector runs that Shepherd does not serve.

API: `PipelineService.ValidatePipeline` (§12) runs stages 1–2 only (fast, used by the editor's Validate button and on-type debounce); the actual save/update/enable endpoints run all three stages server-side regardless of what the client did. With no Alloy binary configured, the `alloy validate` half of Stage 2 is skipped and reported, not passed: `ValidatePipelineResponse.skipped_stages` lists `2` (#209), the editor shows *Syntax checked — alloy validate skipped*, and `shepherd validate` prints the same.

Git-synced files that fail validation: keep the previous good pipeline revision, set `repo_links.sync_status='error'` with details, surface in UI. Never partially apply a commit: a sync applies all files of a commit transactionally or none (as built since #360, §10 "Removed files"). A git sync's Stage 3 is one dry run per commit: the link's collector's merged config with every create, update and delete of the commit applied, assembled as the serve paths assemble it (`serve.Assemble`: role enforcement and the D6 baseline included), then Stages 1–2. Unlike a UI save — where role enforcement only warns, since matchers select many collectors — a committed file whose signals the link's collector's role does not allow refuses the commit: a git pipeline is served to that one collector only, so it would sync "ok" and do nothing. **Only new errors refuse a commit.** When the commit's merged config fails Stages 1–2, the collector's current merged config (same inputs, without the commit, same `serve.Assemble` path) is validated too, and the commit is refused only for a diagnostic inside one of its own files' blocks or one the current config does not have. Diagnostics are matched by the pipeline whose declare block contains them (`merge.Locate`), their line and column in that pipeline's own text, and their message with the temp file `alloy validate` read (and the position quoted after it) removed — never by merged-config line, which shifts when the commit adds or removes a block; outside every block (header, D6 baseline) the message alone is the key. Two cases always refuse: a run that gave no verdict (the dry run's budget ran out, or `alloy validate` timed out, could not start, or failed without a positioned diagnostic — `validate.Result.Inconclusive`; "validation could not complete"), and a config that already fails Stage 1 before the commit (Stage 2 never runs on it, so the commit cannot be checked; the broken pipeline is named and must be fixed first). A commit that passes this way is applied (`last_commit` advances) and the link shows `sync_status='warning'` with `sync_error` naming the pipelines that were already broken. That commit **is served**: the served-config recompute runs Stage 1 only (§6.3), so the collector receives a merged config that `alloy validate` rejects, will likely reject it at load and keep running its previous config until the named pipelines are fixed — the warning says so. The warning stays until the next commit: unchanged-commit polls keep it, and `shepherd_gitsync_total` counts each such poll as `result="warning"` (as it does every poll of a guard-held commit). The second validation runs only when the first fails. Diagnostics name a file of the link by its path, with line and column in the file itself.

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

**Sync reconciler (`gitsync`):** *(Original design, superseded where they differ by the as-built "Removed files" paragraph below — whose validation, transaction and failure rules apply.)* one goroutine ticks every 15s, picks due repo_links (`last_synced_at + poll_interval < now`), processes each: get latest commit for path@branch; if unchanged, touch `last_synced_at` and done. Else fetch all `*.alloy` files under path, validate each (stages 1–2), then upsert pipelines: `name = <linkslug>/<filename-without-ext>`, `source='git'`, `repo_link_id`, `enabled=true`, bump revision if content changed; delete git pipelines of this link whose file disappeared; run stage-3 merge validation for the target collector with the whole new set — on failure, roll the transaction back, keep old state, set `sync_status='error'`. On success set `last_commit`, `sync_status='ok'`, dirty the serve cache.

**Removed files (as built, B-GITSYNC-PRUNE).** A git pipeline's identity is its name, derived from its file's path (path relative to the repository root, leading `/` and `.alloy` dropped); a pipeline of the link whose name no `.alloy` file of the fetched tree derives is *removed*. A file skipped as larger than `max_file_bytes` is still in the tree and keeps its pipeline. Removed pipelines are computed before any file is written and left out of the commit's Stage-3 dry run, so a rename — including one onto the same sanitised name, `My-App.alloy` → `my_app.alloy` — is a delete of the old pipeline and a create of the new one in the same sync. **A commit applies whole or not at all (#360).** Each file is validated on its own first (Stages 1–2, ownership, sanitised names — for an error that names the file), then the commit as a whole: one Stage-3 dry run of the link's collector's merged config with every create, update and delete of the commit in it, role enforcement included (§8). Any failure — a file's own validation, a clash, an error the commit brings to the merged config (§8: errors already there before it only warn), a role refusal — refuses the whole commit: nothing is created, updated or deleted, the collector keeps serving the previous commit's config, `sync_status='error'` with `sync_error` naming the failing files (the first five, then "and K more") and `last_commit` unset, so the next poll fetches the commit again. Only a commit that passes reaches the write: one transaction locks the link row (`GetRepoLinkForUpdate`, the order `DeleteRepoLink` uses — a link deleted mid-sync ends the sync with nothing written), deletes the removed pipelines, writes each created or updated pipeline with its next revision, writes the audit rows — a `pipeline.delete` per removed pipeline with actor `gitsync`, actor type `system`, detail `name`, `reason: "gitsync.file_removed"`, `description`, `repo_link_id`, `git_path` and `commit` (the commit the clone checked out); a `pipeline.create` / `pipeline.update` per written file with detail `repo_link_id`, `git_path` and `commit` — dirties the link's collector (the only collector a git pipeline is served to, §6.1) and records `sync_status='ok'` with that commit. After it commits, the link's collector's served config is recomputed eagerly (the same per-collector path `DeleteRepoLink` uses); no other collector is touched. Before anything is written, every planned create is checked against the sanitised names (`pipelines_org_sanitized_name_key`) of the org's other pipelines — any source, any collector, disabled ones included, minus the ones this sync deletes — and of the other files in the same sync; a clash names the files involved (two clashing new files each name the other) and, like any failure, refuses the commit. Nothing is written either when the fetch fails. **Guards** hold back the **whole commit** — its deletes, new files and updates alike (#360; before it, updates still applied): a moved directory keeps serving its old pipelines (never the old and new copies side by side — duplicate scrapes and remote_write, two receivers on one port). The link gets `sync_status='warning'` with the reason in `sync_error` — naming the guard and how many pipelines the commit would delete — shown under the status on the Git page, and `last_commit` stays unset, so every poll fetches the repository again — a full clone — until the repository or the link changes. They fire when (1) the tree has no `.alloy` files at all while the link owns pipelines — far more often a wrong path or branch than a deliberate wipe; (2) the sync would delete more than 3 pipelines **and** more than half of the link's *baseline* (a moved directory, or a backlog of files removed before the upgrade that shipped this). The baseline is the link's pipelines that existed when its last sync finished `ok`: `repo_links.last_ok_at` (migration 0034) is stamped by a trigger on a write that sets `sync_status='ok'` on an insert, moves the link to `ok`, or records a new `last_commit` while `ok` — not on the unchanged-commit poll or unrelated edits; links that existed at that upgrade start from the upgrade. Pipelines created since are not in it (a sync writes only when it finishes `ok`, so only an older replica's write could add any), so the guard keeps holding on every later poll until the removal shrinks (files restored) or the link is re-created; a link that has never finished `ok` has a baseline of 0, may delete at most 3 per sync, and says so in its own warning. Because the guard compares the branch head with the database, removing fewer files per commit does not help once the files are gone: to apply such a commit, delete and re-create the repo link; or restore some of the removed files, then remove them again a few at a time (at most 3, or no more than half of what remains, per sync), letting each sync complete. During a rolling update an older replica (which has no guard) can sync a warned link to `ok`; finish the rollout promptly. `shepherd_gitsync_total` counts such a sync as `result="warning"`, and a link deleted mid-sync as `result="link_gone"`.

Git pipelines are **read-only in the UI** (view + revision history only; edits happen in the repo). Git and UI/wizard pipelines coexist and merge together per §6.2.

---

## 11. Wizards (`internal/wizard`)

A wizard is a typed, versioned generator: input schema (zod on the client, mirrored Go struct with validation on the server) → rendered Alloy pipeline(s) via `text/template` templates embedded in the binary. Saving a wizard result creates normal `pipelines` rows with `source='wizard'`, `wizard_kind`, and `wizard_state` (the raw input JSON) so it can be re-opened, edited in the wizard, and re-rendered (creating a new revision). Whoever may edit the pipeline (an org editor or above, or a member of its owning team — `UpdatePipeline`'s rule) may also **detach** it from its wizard: `PipelineService.DetachFromWizard`, in place (same id, revisions, owner, matchers, enabled), sets `source='ui'` and clears `wizard_kind`, `wizard_state` and the render fingerprint, writes a "detached from wizard" revision and a `pipeline.detach` audit row. One-way. A detached pipeline is no longer regenerated by a destination update. The pipeline page offers it as "Detach from wizard" behind a confirm dialog (§11.4).

Every wizard write (`CommitWizard`, a destination update's re-render, `shepherd admin rerender-destinations`) stores `pipelines.wizard_render_sha256`, the hex sha256 of the exact contents it wrote (migration 0030). An editor write (`UpdatePipeline`, `RestoreRevision`) keeps it only while the written contents still hash to it and clears it otherwise; the Git sync writes only git pipelines and never sets it.

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
// remote.kubernetes.secret + the writer: rendered by wizard.RenderWriter, §11.4
remote.kubernetes.secret "metrics_auth" {          // only for a Secret auth_mode
  namespace = "{{ destination.secret_namespace }}"
  name      = "{{ destination.secret_name }}"
}
prometheus.remote_write "metrics" {
  endpoint {
    name = "{{ destination.name }}"
    url  = "{{ destination.url }}"                  // the full push URL, verbatim
    basic_auth {                                    // auth_mode = basic_secret
      username = convert.nonsensitive(remote.kubernetes.secret.metrics_auth.data["username"])
      password = remote.kubernetes.secret.metrics_auth.data["password"]
    }
  }
}
```

As built, every wizard writer (`prometheus.remote_write`, `loki.write`) is emitted by one function,
`internal/wizard.RenderWriter`, from the org's destination row the wizard's `*_dest_name` field
names. A name the org does not have, or a destination of the wrong type, is refused
(`failed_precondition`). The destination's `tenant_id` and TLS options are rendered too (§11.4,
#261).

### 11.3 Logs template (shape)

`discovery.kubernetes` (role pod, namespaces) → `discovery.relabel` (namespace include/exclude, container regex, standard k8s label mapping) → `loki.source.kubernetes` → optional `loki.process` (multiline / JSON level stage) → `remote.kubernetes.secret` (Secret modes only) + `loki.write` with the destination's `url` verbatim and the auth block per §11.4.

### 11.4 Destination credential convention

Destinations reference a Kubernetes Secret that must already exist on every spoke cluster (the
platform already distributes such secrets). Shepherd stores only the Secret's name/namespace and
non-sensitive metadata. Rendered configs read it at runtime via `remote.kubernetes.secret`, so **no
telemetry-backend credential ever enters Shepherd's database or the served config text**.

**URL.** The destination's `url` is the writer's full endpoint URL (e.g. `…/api/v1/push`,
`…/loki/api/v1/push`). It is rendered verbatim as a string literal. A URL is not a credential, so it
is not read from the Secret. (The earlier sketch above read `url` from the Secret and appended a
path. Pre-#229 wizards emitted `sys.env("SHEPHERD_DEST_<NAME>_URL")`, which nothing set.)

**Org tenants (0039).** An organisation owns several tenants and tenant prefixes
(`org_tenants`): each row is an **exact** tenant, or a **prefix** under which the org owns every
valid tenant. An org *owns* a tenant when it is one of its exact tenants or a valid tenant
(`gateway.ValidateTenantID`) under one of its prefixes — `gateway.OrgOwnsTenant`, the one function
every check below asks. A tenant belongs to at most one org (D11): an exact tenant is unique across
all orgs (a unique index), and a prefix may not overlap another org's prefix (neither a prefix of
the other), cover another org's exact tenant, nor may an exact tenant fall under another org's
prefix (`gateway.TenantsOverlap`). Exact tenants follow `ValidateTenantID`; a prefix follows
`ValidateTenantPrefix` (same charset, non-empty, at most 149 bytes). At most one exact tenant per
org is its **default**: what a tenant route uses when it names none, what `RenderChartValues`
writes as the chart's `tenant`, and what the destination form pre-fills.

Only an app admin changes them — `AdminService.AddOrgTenant` (optionally making an exact tenant the
default), `RemoveOrgTenant` and `SetDefaultOrgTenant`; `CreateOrg`'s `tenant_id` is an optional
initial default. Each change writes an `org.tenant.add` / `org.tenant.remove` /
`org.tenant.set_default` audit row in its transaction. `AddOrgTenant` refuses (`failed_precondition`)
an overlap, naming the other org (only an app admin sees it), and a tenant or prefix covering a
tenant another org's destination or destination binding already sends (generic, not naming the
org). `RemoveOrgTenant` refuses (`failed_precondition`) only when a live (not revoked) tenant
route, destination or destination binding of the org would be left on a tenant the org no longer
owns — one the row covers and no remaining rule of the org does — listing each. The default
cannot be removed while another exact tenant could take its place (switch the default first);
removing an org's only exact tenant leaves it with no default, after which a route that names no
tenant is refused. Rotating a route re-checks its tenant under the same lock. Every ownership change takes one exclusive transaction-scoped advisory lock
(`LockOrgTenantsExclusive`, class `advisorylock.TenantID`); every write that reads ownership before
writing — a destination, a destination binding, a tenant route — takes the same lock shared
(`LockOrgTenantsShared`), so no check and write can race past an ownership change.
`Org.tenants` and `MeService.GetMe`'s `OrgMembership.tenants` (read-only) carry the rows.

**Strict tenant mode (0040).** In strict tenant mode, destinations, destination bindings,
tenant routes and pipelines may only use the organisation's own tenants (`gateway.OrgOwnsTenant`
over its `org_tenants`). It applies to an org when its `orgs.strict_tenants` flag is on, or to
every org when the server-wide config `tenants.require_owned` is true (chart
`config.tenants.require_owned`, default false); `internal/tenancy` is the one definition
(`tenancy.Policy`). Off, everything is exactly as without it. On, for org O:

- every destination create and update, and every destination binding create and update, with a
  non-empty tenant O does not own is refused (`invalid_argument`, "strict tenant mode: …"),
  checked inside the writing transaction under the shared ownership lock, after the cross-org
  check. A destination with no tenant is unaffected, so an org that owns no tenant can still have
  destinations, just none that send one;
- the render-time re-check (`wizardDestinations`) refuses a destination whose tenant O no longer
  owns, so no wizard render or commit, destination re-render or `shepherd admin
  rerender-destinations` run writes it;
- tenant routes already use only an owned tenant (below);
- the validation gate's tenant lint (§8) refuses pipeline text that sets a literal tenant O does
  not own, on every save path, and Stage 3 lints every pipeline a change would enable, replace or
  restore — so a pipeline saved before the org became strict is refused when it is enabled.

Only an app admin changes the flag: `AdminService.SetOrgStrictTenants` (audited as
`org.strict_tenants`). Turning it on takes the **exclusive** ownership lock and runs the
preflight (`tenancy.Preflight`): it is refused (`failed_precondition`, listing each) when O owns no
tenant, or while any live tenant route, destination or destination binding of O is on a tenant O
does not own, or any **enabled** pipeline of O fails the tenant lint (disabled ones are linted when
enabled). Turning it off always succeeds. `AdminService.TenantPreflight` (app admin, read-only)
returns the same list as data — for one org, or with `org_id` empty for every org, as
`tenants.require_owned` would see it. With `tenants.require_owned` on, `shepherd serve` runs that
all-org preflight at boot and **refuses to start** while anything is listed (fail closed), naming
each object and how to clear it (assign the tenant to its org, or edit, disable or delete the
object); `shepherd admin tenant-preflight` prints the same list without starting the server and
exits non-zero while anything is listed. `Org.strict_tenants` is the flag, `Org.tenants_required`
and `OrgMembership.tenants_required` the effective state; the destination form then offers only
the org's tenants. Every pipeline write (create, update, restore, enable, wizard commit, a
destination re-render, `rerender-destinations`, a git sync's apply) re-runs the lint in its writing
transaction under the shared ownership lock (`tenancy.LintLocked`), so it serialises with the
toggle and with ownership changes. Each enabled pipeline's literal tenants are kept in `pipeline_tenants` (0040) with a
`pipeline_tenant_index` row naming the contents' sha256 and whether the lint could read them all;
every pipeline write records them in its transaction, and the server brings the table up to date
at every boot (its backfill). Recording **fails soft**: a pipeline whose tenants the lint cannot
read within its bounds is recorded as incomplete and logged, never a failed write or boot (an
enabled pipeline of a strict org is fully readable, or the gate would have refused it).
`AddOrgTenant` first re-records every other org's enabled pipeline whose index row is missing or
names other contents (a pod that predates the index wrote it), then asks one indexed query.
Ownership changes see pipelines too: `RemoveOrgTenant` on an org
in strict mode refuses while an **enabled** pipeline would be left writing to a tenant the org no
longer owns, and `AddOrgTenant` (for any org) refuses a rule covering a tenant another org's
enabled pipeline writes to (`validate.TenantLiterals`), naming each. Strict mode covers what
Shepherd writes and renders; it is not containment against a collector configured outside
Shepherd, nor against a tenant computed at run time — see the lint's limits in §8.

**Tenant routes.** `CreateTenantRouteRequest.tenant` (field 8; field 2, the old free-form
`tenant_id`, stays reserved) must be a tenant the org owns (`invalid_argument` otherwise); empty
means the org's default, and an org with no default is refused (`failed_precondition`). The route
stores the resolved tenant; rotation keeps it.

**Tenant (#261).** A non-empty `tenant_id` is sent as `X-Scope-OrgID`:
`headers = { "X-Scope-OrgID" = "<tenant>" }` in a `prometheus.remote_write` endpoint, `tenant_id =
"<tenant>"` in a `loki.write` endpoint. Empty sends none. The API refuses (`invalid_argument`) a
value outside `gateway.ValidateTenantID`'s rule, and one another org owns (exactly or under a
prefix) with the generic message `tenant_id is not available to this org`. A destination binding's
tenant gets the same two checks on create and update. `wizardDestinations` applies the cross-org
check again at render time, so such a destination is refused only by the renders that name it.
The org side is guarded too (`AddOrgTenant` and `CreateOrg`, above). The checks run inside the
writing transaction under the ownership lock, so they cannot race past each other. The guard covers a destination's tenant only: a hand-written pipeline
can still set any header. The served config keeps a tenant until the pipeline is regenerated (a
save of a destination it names); a pipeline naming several destinations is regenerated as a
whole, so saving any one of them also starts sending a tenant stored earlier on another. `MeService.GetMe` returns each org's own
tenants on `OrgMembership.tenants` (read-only, empty when it has none), and the destination
form pre-fills a new destination's tenant from the default one. This is egress from the org's own
collectors, which an org editor could already tag with any header in a raw pipeline; it does not
touch the gateway tier's ingress tenancy (D10/D11, `docs/archive/plans/2026-10-06-destination-tenant-tls.md`
§2). A tenant already stored before #261 reaches a pipeline on its next regeneration, or when an
operator runs `shepherd admin rerender-destinations --all` (dry run unless `--apply`; lists each
org's destinations with a tenant and the wizard pipelines whose render differs, regenerates them
through the same gate, never overwrites a hand edit; `UPGRADING.md` 0.19.x → 0.20.0).

**TLS (#261).** `extra.tls` (`wizard.ExtraKeyTLS`, no proto change) names objects on the spoke,
never certificate material: `{"ca": {"kind": "configmap"|"secret", "namespace", "name", "key"?},
"client_cert": {"namespace", "name"}, "server_name"}`, every part optional. The CA is read from
`key`, default `ca.crt`; the client certificate Secret holds `tls.crt` and `tls.key`
(`kubernetes.io/tls`). Rendered into the writer's `tls_config`: `ca_pem` (through
`convert.nonsensitive` when read from a Secret), `cert_pem = convert.nonsensitive(…["tls.crt"])`,
`key_pem = …["tls.key"]`, `server_name`, after a `remote.kubernetes.configmap`/`secret` per distinct
object (an object named twice — auth and client certificate in one Secret — is read once).
`wizard.ParseTLS` decodes strictly: an unknown key is refused, so there is no
`insecure_skip_verify`. The API refuses (`invalid_argument`) a malformed `extra.tls`, a reference
that is not a valid namespace/name, a client certificate that is not a Secret, an illegal CA key,
a non-hostname `server_name`, and any TLS option on a URL that is not `https://`.

**Key contract (#229)** — `internal/wizard.SecretKeys`:

| `auth_mode` | Secret keys | Rendered into the writer's `endpoint` |
|---|---|---|
| `none` | — | nothing |
| `basic_secret` | `username`, `password` | `basic_auth { username = convert.nonsensitive(data["username"]), password = data["password"] }` |
| `oauth2_secret` | `client_id`, `client_secret`, `token_url` | `oauth2 { client_id = convert.nonsensitive(…), client_secret = data["client_secret"], token_url = convert.nonsensitive(…) [, scopes] }` |

OAuth2 scopes are not a Secret key. Shepherd never reads the Secret, so it cannot render an optional
key conditionally. Scopes are non-sensitive destination metadata, `extra.oauth2_scopes` (a list of
strings). The API refuses (`invalid_argument`) an unknown `auth_mode`, and a Secret mode without a
valid Kubernetes namespace and name. A stored row whose `extra` does not decode (written before that
validation, or by a path that skipped the API) is refused only by the wizard renders that name it,
never by every render in the org (#261).

**When it is rendered.** At wizard render/commit time (`RenderWizard`/`CommitWizard` load the org's
destinations), so the auth block is part of the stored pipeline and passes Stages 1–3 like any other
line. `UpdateDestination` re-renders every wizard pipeline in the org whose `wizard_state` names
the destination under a `*_dest_name` key, from that stored state, in the same transaction as the
destination update (#262): Stages 1–2 per pipeline, Stage 3 over the merged config with all of them
swapped in, then a revision, a `pipeline.rerender` audit row (the editing user) and the org's serve
cache marked dirty. Any refusal fails the update (`failed_precondition`, naming each pipeline) and
nothing changes. Each refused pipeline is named in quotes, with what to do: a wizard pipeline
that was edited by hand is refused the same way, never overwritten, and the message names the
three actions its page offers: restore its last wizard-generated revision, detach it from the
wizard (§11), or delete it; a render or validation failure names changing the destination, detaching
or deleting. Running the wizard again is never suggested: it creates a new pipeline. It is
hand-edited when its contents no longer hash to its render fingerprint
(`wizard_render_sha256`); with no fingerprint (written before migration 0030, or cleared by an
editor write) the check falls back to comparing it, byte for byte, with its render against the
destinations as they stood before the update, or with that render with every destination's tenant
and TLS option cleared (the pre-#261 renderer rendered neither) — the latter only while the pipeline's
latest wizard-written revision (`created` / `re-rendered: …`) still holds exactly its contents, so
a hand edit that deleted a rendered `X-Scope-OrgID` is not mistaken for old output. That fallback
is also tried when the render against the old destinations fails on a stored tenant (one outside
today's rule, or one another org has since been given), so the update that clears it goes through. A pre-#229 `sys.env` pipeline is never treated as
hand-edited; it is converted. A rename rewrites the name in those pipelines' `wizard_state` as well.
`DeleteDestination` refuses (`failed_precondition`, listing them quoted, and saying to detach them
from the wizard or delete them first) while any wizard pipeline in the org names the destination.
Both refusals also carry the pipelines as a Connect error detail, a `google.protobuf.Struct`
`{"pipelines": [{"id", "name"}]}` (no new proto message); the destinations page links each one to
its page from it, never by parsing the message. Every revision records the pipeline's render
fingerprint as it stood when written (`pipeline_revisions.wizard_render_sha256`, migration 0031),
so it is set exactly on revisions whose text a wizard wrote; `RestoreRevision` of such a revision
copies it back to the pipeline, re-attaching it to its wizard even when the wizard renders that
state differently today (a restored revision without one falls back to the fresh-render check
above). `RestoreRevision` refuses (`invalid_argument`) a caller change note of `created` or one
starting `re-rendered:`, the notes only wizard writes use. Pipelines rendered before #229 (the
`sys.env` writer) are converted once by `shepherd admin rerender-destinations`.

**Following the wizard, in the API and the UI (#301, #309).** `Pipeline.follows_wizard`
(output-only, set on every response that returns a `Pipeline`) is the hand-edit check above, run
against the org's current destinations: false for a pipeline it calls hand-edited and for every
non-wizard pipeline, true when the text is still the wizard's. When the check cannot decide — an
unknown wizard kind, stored state that does not decode, a render that fails against the current
destinations (a deleted destination, an `extra` that does not load), a failed read — it is
**true**, and the server logs the reason at Warn: true means "not known to be hand-edited", and a
destination update still refuses such a pipeline and names why. A pipeline written before the
fingerprint by a wizard whose output has since changed (#289) reads false although its current
revision is the wizard's own. `UpdateDestination`'s response carries
`Destination.regenerated_pipelines`, the number of wizard pipelines it re-rendered (zero in every
other response); the UI's toast reads "Destination updated — N pipeline(s) regenerated", or just
"Destination updated" when none was. Everything the UI shows about it reads `follows_wizard`,
never the text: a wizard pipeline with `follows_wizard` false carries a *Not following wizard*
badge in the pipelines list ("Destination changes no longer regenerate this pipeline; open it to
see why" — the list has no revisions, so it cannot tell a hand edit from an older wizard's text)
and one in its header that says which: *Hand-edited*, or *Not following wizard* when its current
revision is the wizard's own, since nobody edited it (until the history loads, the list's
wording). Its page offers "Restore last wizard
version" (`RestoreRevision` of the newest revision its wizard wrote, by the notes above, even
when that is the current revision: the restore copies the revision's fingerprint back and
re-attaches it; the confirmation says when it also changes the matchers or the enabled state).
Saving asks "Save a hand edit?" first exactly when `follows_wizard` is true and the save changes
the text; the page takes the follow state from the save's and the restore's own responses, so a
second save made at once asks the right question.

**RBAC.** The collector's ServiceAccount needs `get` on `secrets` in the Secret's namespace:
`remote.kubernetes.secret` GETs the one named Secret at load and on each poll and never lists or
watches (`list`/`watch` are harmless extras). The `grafana/alloy` chart's default ClusterRole grants
Secret access cluster-wide. A collector with restricted RBAC needs a Role and RoleBinding in that
namespace (`e2e/k8s/destination_auth_test.go` runs with get/list/watch on secrets). A TLS CA in a
ConfigMap needs `get` on `configmaps` in its namespace (`remote.kubernetes.configmap` reads the same
way; `e2e/k8s/destination_tls_test.go` grants get/list/watch on configmaps).

**Missing data keys.** A key the Secret or ConfigMap does not hold reads as null, which Alloy takes
as "not set". A missing `tls.crt` or `tls.key` still refuses the load (the other half of the pair
is then unmatched); a missing CA key does not — the collector runs with the system roots and fails
at send time with x509 errors. No Alloy expression can make the CA fail at load: a ConfigMap's data
is not yet there when the writer is first evaluated (verified on Alloy v1.20.1).

---

## 12. Management API (Connect `shepherd.mgmt.v1`)

The management API is the Connect contract `shepherd.mgmt.v1`: 14 services defined in
`proto/shepherd/mgmt/v1/*.proto`, generated for Go (`gen/`) and TypeScript (`web/src/gen/`), and
served by `internal/mgmtapi` (`MountRPC`). The SPA and every machine caller use it. A unary call is
`POST /shepherd.mgmt.v1.<Service>/<Method>` with a JSON request body (the Connect protocol), so
integrators need no generated client: field names are lowerCamelCase, zero values are omitted from
responses, and errors are `{"code": "<snake_case>", "message": "..."}` with the Connect HTTP status
mapping (401 `unauthenticated`, 403 `permission_denied`, 404 `not_found`, 409 `already_exists`, ...).
Mutating calls carry the CSRF header (§7); machine callers authenticate as a service account (§7.3a).

**Authorization.** `procedureRequirements` in `internal/mgmtapi/rpc_interceptor.go` is the
authoritative per-procedure table of `auth.Requirement`s (§7.2); the interceptor fails closed for any
procedure missing from it, and `testdata/procedure_requirements.golden` pins every entry's effective
semantics for a human session and a service account. "Org viewer/editor/admin" below is
`RequireOrg(role)`, which an app admin also clears. Roles and tenant-route statuses are enums on the
wire (`Role`, `TenantRouteStatus`, `TenantRouteApplyStatus`). The floor per service:

| Service | Floor |
|---|---|
| `MeService` | any authenticated caller |
| `AdminService`, `UserService` | app admin — except `SearchGroups`, `ListAgentTokens`, `CreateAgentToken` and `RevokeAgentToken`: app admin, or org admin of the request's `org_id` (§7.3) |
| `FleetService`, `DestinationService`, `TenantRouteService`, `TeamService` | reads: org viewer · writes: org admin |
| `PipelineService` | reads: org viewer · writes: org viewer at the interceptor, then a per-pipeline check in the handler (`auth.AuthorizeOwnership`) — an org editor or above may write any pipeline, anyone else only through the team owner-write grant (§7.2), a pipeline owned by a team they belong to; `ValidatePipeline` and `FormatPipeline` stay org viewer; `SetPipelineOwner` is org admin |
| `WizardService`, `SimulateService` | org editor |
| `VisualService` | `Render`, `Validate`, `UpgradeCheck`: org viewer at the interceptor, then decided in the handler — with a `pipeline_id`, the same per-pipeline check as a `PipelineService` write (an owning-team member may use the visual builder on their team's pipeline); without one (a new, unsaved graph), org editor · `GraphView` and `DiffRevisions`: org viewer |
| `GitOpsService`, `AuditService`, `ServiceAccountService` | org admin |

**What `/api` still serves.** Only what the Connect contract deliberately leaves out
(`docs/archive/api-contract-design.md`, "Out of contract, unchanged"):

```
GET    /api/schema/current                       [any]  pinned Alloy component schema + overlay (ETag cached)
GET    /api/schema/{version}                     [any]  one schema version + overlay (ETag cached)
GET    /api/version                              [none] build version
POST   /api/auth/local/login, /api/auth/local/password   local sign-in and password change (§7.2)
```

The plain-JSON REST shim that duplicated the Connect procedures under `/api/*` was deprecated in
v0.9.0 and **removed in v0.11.0**; its routes now answer 404.

---

## 13. Frontend — explicit layout, design system, and editor spec

This section is prescriptive. Do not restyle, do not pick different components, do not "improve" the visual design. Every screen uses ONLY shadcn/ui components + Tailwind utilities with the tokens below.

### 13.1 Design system (fixed tokens)

- **Theme**: follows the OS `prefers-color-scheme` until the user toggles (D8, 2026-09-11 — `web/src/theme.ts`); the topbar toggle stores an explicit override in `localStorage`, applied via the `class` strategy on `<html>`; dark is the fallback when no preference is readable. All colors below are Tailwind palette names — implement both modes with CSS-variable theming; do not hand-pick hex values elsewhere.
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
- **Topbar**: left = breadcrumbs (auto-derived from the route, `web/src/components/breadcrumb.ts`). Right, in order: **org switcher** (a plain `<select>` of the user's orgs, rendered only when they have more than one; App Admin additionally has an "All orgs" pseudo-entry on Collectors/Audit), theme toggle (`Sun`/`Moon` ghost icon button), **Sign out** button. There is deliberately no avatar, user dropdown or role badge (struck 2026-09-11) — the persona's roles show on the pages they gate, not in the chrome.
- Selected org is part of the URL state via a `?org=` search param managed by TanStack Router; switching org preserves the current route.

### 13.3 Shared patterns (use everywhere, never ad-hoc)

- **Tables**: the `DataTable` primitive in `web/src/components/ui/` (no TanStack Table). Sticky header row (`bg` = surface), sortable columns show `ArrowUpDown`, hover row `bg-zinc-900/60`, whole row clickable when a detail page exists (`cursor-pointer`, plus an explicit chevron cell). Column of actions = right-aligned ghost `MoreHorizontal` dropdown. Pagination footer: "1–25 of 214" + Prev/Next, page size fixed 25.
- **Filter bar** above every table: left-aligned row of controls `gap-2` — a `w-64` search Input with `Search` icon, then Select/Combobox filters, then active filter chips (dismissible Badge), right-aligned primary action Button (e.g. "New pipeline"). Filters sync to URL search params.
- **Loading**: shadcn Skeleton — tables render 5 skeleton rows; cards render skeleton blocks. Never a full-page spinner.
- **Empty states**: centered in the content card, `py-16`: muted icon (size 32), one bold line, one `text-zinc-400` line, one primary action. Exact copy per screen is given in §13.5.
- **Errors**: query errors render an inline Alert (destructive) with the server `error.message` and a Retry button — never a toast for load failures. Mutation results DO use toasts (shadcn `sonner`): success = short confirmation; failure = error message. Validation failures render inline (§13.6), not as toasts.
- **Dialogs**: shadcn Dialog for create/edit forms (`max-w-lg`); AlertDialog for destructive confirms — title "Delete <name>?", body states blast radius, confirm button destructive variant, and for high-risk actions (unclaim cluster, delete pipeline, revoke token) require typing the resource name into an input to enable the button.
- **Forms**: the `Field`/`Input` primitives with hand-written state (no react-hook-form, no zod); labels above inputs, `text-xs text-zinc-400` help text below, field errors in red-500 `text-xs`. Submit = primary right-aligned; Cancel = ghost left of it.
- **Timestamps**: always relative ("4m ago") with absolute ISO on hover (Tooltip). **Hashes/IDs**: JetBrains Mono, first 10 chars + copy-on-click button (`Copy` icon, toast "Copied").

### 13.4 Route tree

21 routes (`web/src/routes/router.tsx`; role floors mirror `internal/mgmtapi/rpc_interceptor.go`'s per-procedure requirement — see `web/src/routes/routeManifest.ts`):

```
/login                      — sign-in page
/                           — Overview
/collectors                 — Collectors list
/collectors/:id             — Collector detail (tabs: Instances | Served Config | Pipelines | Access)
/pipelines                  — Pipelines list
/pipelines/new              [editor] — Editor (create)
/pipelines/:id              — Editor (view/edit; read-only for viewers & git-sourced)
/pipelines/:id/visual       — Visual builder graph editor
/pipelines/:id/graph        — Graph view (read-only; `declare` modules drawn as collapsible groups, ELK layout — docs/visual-builder-design-VB1.md §4.3)
/pipelines/visual/new       — Visual builder (create)
/wizards                    [editor] — Wizard gallery (cards, one per registered wizard kind)
/wizards/:kind              [editor] — Wizard stepper (one generic runner for every registered wizard)
/destinations                — Destinations list + create/edit dialogs
/teams                       [viewer] — Teams (membership, local + IdP-group-backed)
/git                          [admin] — Tabs: Repo links | Credentials
/admin/orgs                    [app-admin] — Orgs
/admin/clusters                 [app-admin] — Cluster claiming
/admin/tokens                    [org-admin] — Agent tokens (an app admin sees every org's and binds)
/admin/users                      [app-admin] — Local users
/admin/auth                        [app-admin] — Single sign-on (§7.1b)
/audit                              [admin] — Audit log (org-scoped)
```

Unmarked routes carry no floor beyond an authenticated session with some access to the current org. Role-based rendering: every write affordance (buttons, switches, menu items) is hidden — not disabled — for users lacking the role; admin routes and the routes above tagged with a floor additionally redirect to `/` with a toast and a `[data-testid="route-denied"]` element on direct navigation (client-side guard, `RequireRole`) — the server remains the actual enforcement in both cases.

### 13.5 Screen-by-screen specification

**Login** (`/login`): centered card `max-w-sm` on the page background: product mark, "Sign in to Shepherd" (`text-lg font-semibold`), one full-width primary button "Continue with <provider>" → `GET /auth/login`, where the label comes from `/auth/methods`'s `oidc_display_name` (§7.1) rather than being hardcoded. Footer caption names the same provider's groups. Auth-failure query param renders a destructive Alert above the button; `auth_error=oidc_not_configured` says so specifically and points at Admin → Single sign-on.

**Overview** (`/`): row of four stat cards (grid `grid-cols-4 gap-4`; each: `text-xs text-zinc-400` label over `text-2xl font-semibold tabular-nums` value + small context line): Collectors (value = total, context = "n healthy · n stale"), Active pipelines, Clusters, Sync errors (red value when > 0). Below, two half-width cards side by side: **"Needs attention"** — table of collectors with status Failed or zero live instances (columns Cluster, Role, Status, Last seen; empty state: `CheckCircle2` icon, "All quiet", "Every collector is applying its config.") — and **"Recent changes"** — last 10 audit rows (actor, action verb + resource as one sentence, relative time).

**Collectors list**: filter bar (search by cluster; Role Select; Status Select; org implicit). Columns: Cluster, Role (icon+name badge), Instances (live count; amber text when 0), Version (distinct `alloy_version`s, comma-joined), Status (badge = worst status across instances), Last seen, Served hash (mono short). Empty state: `Radar` icon, "No collectors yet", "Collectors appear here automatically when a spoke cluster registers with a valid agent token.", button "View onboarding snippet" → dialog with the §1 YAML in a code block.

**Collector detail**: header block: `text-xl` "`{cluster}` / `{role}`" with role icon, org Badge, status Badge, and assigned-group chips (Badge outline with `Users` icon). Tabs (shadcn Tabs, underline style):
- *Instances*: table — Instance ID (mono, copyable), Name, Version, OS, Status badge, Error (`AlertTriangle` amber icon with Tooltip showing `remote_config_error`), Last seen. Rows with `unregistered_at` set render at 50% opacity with an "Unregistered" badge.
- *Served Config*: meta row (hash copyable, generated-at, "Built from n pipelines" where each pipeline name links to its editor) above a read-only editor (§13.6) filling remaining height.
- *Pipelines*: compact table of currently-matching pipelines: Name (link), Source badge, Enabled switch (org editor+; flipping calls enable/disable and refetches), Revision, Updated.
- *Access* (visible to org admin): current assignments as a list of rows (group name, GUID mono, remove `X` button w/ confirm); "Assign group" opens a Combobox dialog backed by `GET /api/admin/groups/search` (debounced 300ms, min 2 chars, shows name + GUID).

**Pipelines list**: filter bar (search name; Source Select; Enabled Select). Columns: Name, Source badge, Matchers (each matcher as a mono chip, max 3 shown then "+n"), Enabled (switch, org editor+), Revision, Updated by/at. Primary action "New pipeline"; secondary ghost "Open wizard" → `/wizards`. Empty state: `Workflow` icon, "No pipelines", "Create a pipeline by hand or start from a wizard.", two buttons.

**Pipeline editor** (`/pipelines/new`, `/pipelines/:id`): full-height two-pane split (left `w-[380px] shrink-0 border-r overflow-y-auto p-6`, right flex-1 editor column).
- Left pane, top→bottom: Name input; **Matcher builder** — vertical list of rows [key Combobox | operator Select (`=`,`!=`,`=~`,`!~`) | value Combobox | remove ghost `X`], "+ Add matcher" ghost button; key/value comboboxes are fed by `GET /api/orgs/{org}/attributes` (§12) and remain free-text-capable; beneath it a live **match preview** card: "Matches **n** collectors" + up to 5 `cluster/role` mono lines + "+n more" (calls preview endpoint, 500ms debounce; n=0 renders the count in amber with caption "This pipeline currently matches nothing."). Then: Enabled switch with caption "Enabling validates against every affected collector."; Source badge; Revision Select (rev list w/ author+time) — a "View diff" button per revision opens a read-only CodeMirror merge view (§13.6's `RevisionDiff`, old revision left vs current editor contents right) in place of the editor in the right pane, with a caption of lines added/removed; a "Restore this revision" button (org editor+) opens a confirm dialog and, on confirm, calls `RestoreRevision`, which writes a **new** revision from the old one's contents and matchers (never rewrites history; the pipeline keeps its current enabled state, #377 — when the revision was saved in the other state the dialog says the current one is kept) and reloads the editor with it. Git-sourced pipelines can be restored too — the dialog adds a warning that the next git sync overwrites the restore. Graph diff for visual pipelines is not built; the diff view is text-only (a visual pipeline's restore still restores its graph, since `wizard_state` travels with the revision (for revisions written after migration 0019; older rows carry no graph, so restoring one restores text only and leaves the stored graph as it was) — only the *diff* is text-only). Danger zone card at bottom: Delete (AlertDialog w/ typed confirm).
- Right pane: toolbar row (`h-11 border-b px-3` flex): left = validation status — live-region text: spinner+"Validating…", or `CheckCircle2` emerald "No problems", or `XCircle` red "n problems"; right = ghost "Format" (server-side `alloy fmt` through a `FormatPipeline` RPC), outline "Validate", primary "Save". As of v0.5.0 only Save is built and validation runs on an idle debounce; Format and Validate are scheduled (decision 2026-09-11, ledger §4). Below: the editor (§13.6) `flex-1`. Bottom: collapsible **Problems panel** (`max-h-40`, monospace rows `line:col  message`, click scrolls editor to the position).
- Save with stage-3 failure → Dialog "Validation failed on n collectors": Accordion per collector containing its diagnostics; single button "Back to editing".
- Git-sourced pipelines: entire left pane read-only, editor read-only, and a violet banner across the top: `GitBranch` icon + "Managed in Git — {project}/{repo} @ {branch}{path}" with an external link to ADO and a "Last synced 2m ago · ok" caption.
- Viewers see the same screen fully read-only with no Save/Delete/switch affordances.

**Wizard gallery**: grid of cards `grid-cols-3`, one card per registered wizard kind (`internal/wizard` — six today: application observability, cluster metrics, pod logs, database, blackbox, self-monitoring), each with icon, title, description and a "Start" button. No placeholder card.

**Wizard stepper** (`/wizards/{kind}`): one generic runner renders whatever `GetWizardSchema` returns for that kind, so steps and fields are backend-defined — the step list below describes the *intended* app-observability flow, not a UI contract (as shipped it is: 1 Scrape targets, 2 Log collection, 3 Destinations, 4 Collector matching, then Review). Left vertical stepper rail (`w-56`): numbered circles (active = indigo filled, done = emerald check, upcoming = zinc outline) + step labels exactly: 1 Scope, 2 Namespaces, 3 Metrics, 4 Logs, 5 Destinations, 6 Review. Steps 3/4 auto-skip (rendered struck-through) when their signal isn't selected. Right: step content card `max-w-2xl` with footer Back (ghost) / Continue (primary; disabled until the step's zod schema passes). Step specifics: 1 — cluster multi-select (Combobox multi with checkboxes, claimed clusters only) + signal RadioGroup (Metrics / Logs / Both); 2 — namespace chips input (Enter adds; invalid RFC1123 chips render red with tooltip) + include/exclude RadioGroup; 3/4 — the option forms from §11.1; 5 — destination Selects filtered by type, each rendering a summary line (url host, tenant) under the control, plus an inline "New destination" ghost button opening the destinations dialog; 6 Review — for each generated pipeline: name, matcher chips, match-preview line, and a read-only editor with live stage-1/2 diagnostics; footer swaps to "Save as disabled" (outline) + "Save & enable" (primary). Success → toast + navigate to `/pipelines`.

**Destinations**: table (Name, Type badge, URL host, Tenant, Secret `namespace/name` mono, Auth mode, Updated). Create/edit Dialog fields per §5 schema with zod; `type` Select drives conditional fields. Empty state: `Send` icon, "No destinations", "Destinations tell wizard pipelines where to ship telemetry."

**Git** (`/git`): Tabs. *Repo links*: table (Target collector `cluster/role`, Repo `project/repo` mono, Branch, Path, Interval, Last sync (relative + status dot: ok emerald / error red with Tooltip = `sync_error`), Last commit short mono; row actions: Sync now, Edit, Delete). Link Dialog: credential Select, project/repo/branch/path inputs, target collector Select, interval input; "Test & link" button calls verify then creates. *Credentials*: table (Name, ADO org URL, Client ID mono, Created; actions: Test, Edit, Delete). Credential Dialog: name, ADO org URL, tenant ID, client ID, client secret (password input, write-only — edit shows "Leave blank to keep current secret").

**Admin — Orgs**: table (Name, Display name, Admin group (name + GUID), Viewer group, Clusters n, Pipelines n). Create/edit Dialog uses the Graph group Combobox for both group fields.

**Admin — Clusters**: two stacked cards. **Unclaimed** (amber left-border accent): table (Cluster, First seen, Roles seen as role-icon row, Live instances) + row action "Claim…" → Dialog with org Select. Empty: "No unclaimed clusters." **Claimed**: table (Cluster, Org, Roles, Instances) + "Unclaim" (typed-confirm AlertDialog warning that collectors stop receiving org pipelines).

**Admin — Single sign-on** (`/admin/auth`, app admin only): one form over §7.1b's settings, grouped as Provider / Groups and administrators / Claim mapping. A provider picker seeds claim names, scopes, and the button label from the preset catalogue (`ListOidcProviderPresets`) and shows that provider's "what you must configure in the IdP for the groups claim to arrive" note — the likeliest way to misconfigure this. "Test connection" runs discovery without saving and renders the discovered issuer and endpoints. The client secret field is write-only: blank means "keep the stored one", and the typed value is dropped from component state the moment the save succeeds. When the chart owns the configuration every control is disabled and a banner says why — an admin must still be able to *read* which provider their cluster trusts. An inline warning fires while the app-admin group list is empty, because saving in that state leaves the local-admin account as the only way in.

**Admin — Agent tokens**: table (Name, Token ID mono, Created by/at, Status: Active / Revoked badge; action Revoke w/ confirm). "New token" Dialog → on create, swaps to a success view: amber Alert "Copy this secret now — it won't be shown again", two copy fields (Token ID / Secret, mono), and a ready-made YAML snippet block (`auth: {type: basic, username: <id>, password: <secret>}`) with copy button; Close button only.

**Audit**: filter bar (actor search, action Select, date range Popover+Calendar). Table: Time, Actor, Action (mono verb badge e.g. `pipeline.update`), Resource (type + linked name), Detail (`ChevronRight` expands a row-panel rendering the JSON detail in a mono `<pre>`).

### 13.6 Editor: Alloy syntax highlighting, autocompletion, and diagnostics

All editors are ONE shared component `web/src/editor/AlloyEditor.tsx` (props: `value`, `onChange?`, `readOnly`, `diagnostics`, `height`). Packages: `@codemirror/state`, `@codemirror/view`, `@codemirror/language`, `@codemirror/autocomplete`, `@codemirror/lint`, `@codemirror/search`, `@lezer/highlight`, plus `@codemirror/merge` for the read-only revision diff view (`RevisionDiff`, §13.5) — loaded only through the lazy editor chunk, never the entry chunk. The component is code-split behind `web/src/editor/LazyAlloyEditor.tsx` and fetched on first editor mount. Theme: build ONE custom `EditorView.theme` matching §13.1 (zinc backgrounds, JetBrains Mono 13px, active-line `zinc-900/60`, selection indigo at 25% opacity, gutter text `zinc-600`) — do not import a stock theme; derive the light variant from the same definition.

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
shepherd token create --name X --org O [--clusters …] [--roles …] | bind --id ID --org O | revoke <id> | list   # direct-DB agent token mgmt (bootstrap)
shepherd validate <file.alloy> # run stages 1–2 locally
shepherd version
shepherd healthcheck [--addr host:port]   # curl /healthz and exit 0/1 — container HEALTHCHECK, no curl/wget in the distroless image
shepherd dev seed                          # developer-only, direct DB, never exposed over HTTP (§ amendments)
shepherd dev create-session --persona X    # developer-only, direct DB, never exposed over HTTP (§ amendments)
```

Viper: config file `shepherd.yaml` (path via `--config`), env override prefix `SHEPHERD_` (dots→underscores). Full schema with defaults:

```yaml
server:    { listen: ":8080", base_url: "https://shepherd.example.internal" }
database:  { url: "postgres://...", max_conns: 20 }
oidc:      { issuer: "", client_id: "", client_secret: "", redirect_url: "", scopes: [openid, profile, email],
             provider: "entra", display_name: "Microsoft",              # preset key (§7.1a); sets the use_graph_groups default
             subject_claim: "oid", email_claim: "email", name_claim: "name", groups_claim: "groups",
             use_graph_groups: true }                                    # default: true iff provider == "entra"
             # issuer: "" does NOT mean "no SSO" — it hands configuration to app admins via the UI (§7.1b).
auth:      { app_admin_group_ids: [], session_ttl: "8h" }
graph:     { tenant_id: "", client_id: "", client_secret: "" }   # app-mode Graph; may reuse oidc app
agent:     { inactive_after: "", delete_after: "" }   # no viper default; the chart supplies 5m / 24h (values.yaml)
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
| `deployment.yaml` | RollingUpdate (maxUnavailable 0, maxSurge 1). Container args `["serve"]`. Config mounted from the ConfigMap at `/etc/shepherd/shepherd.yaml`, passed via `--config`. Secrets injected as env vars via `envFrom.secretRef` (viper env override handles the rest). Probes: liveness `GET /healthz`, readiness `GET /readyz` (readiness must check DB connectivity + pending-migration state) — both invoked via `shepherd healthcheck` (§14), not `wget`/`curl`, since the distroless image has neither. `securityContext`: runAsNonRoot, readOnlyRootFilesystem, drop ALL caps; an `emptyDir` at `/tmp` (needed by the `alloy validate` temp files, §8). Standard passthroughs: `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `topologySpreadConstraints`, `podAnnotations`, `extraEnv`. |
| `configmap.yaml` / `configmap-migrate.yaml` | Renders `shepherd.yaml` from `.Values.config` (§14 schema) via a shared `shepherd.configYaml` helper — the values structure IS the config structure. Secret-bearing fields (`database.url`, `oidc.client_secret`, `graph.client_secret`, `security.encryption_key`) must NOT appear here; they arrive only as env vars. Checksum annotation on the Deployment pod template (`checksum/config`) to roll pods on config change. **As of chart 0.9.0 the runtime ConfigMap/Secret/ServiceAccount are ordinary tracked resources, not Helm hooks** (an earlier hook design destroyed the database on `helm uninstall`, since a hook resource is not tracked); the migration Job — which still runs as a `pre-install,pre-upgrade` hook and therefore predates the tracked resources existing — gets its own `-migrate` copies of the ConfigMap, Secret and ServiceAccount instead, built from the identical helper so the two cannot drift. `upgrade-guard.yaml` renders no object; it exists only to turn the one-time hook→tracked migration into a named error instead of a silent resource-adoption conflict. |
| `secret.yaml` | Only rendered when `existingSecret` is empty, `externalSecrets.enabled` is false, AND `.Values.secrets.*` is provided (dev convenience — least private of the three options: values stay readable via `helm get values` for the life of the release). Production path: `existingSecret: <name>` referencing a secret with keys `SHEPHERD_DATABASE_URL`, `SHEPHERD_OIDC_CLIENT_SECRET`, `SHEPHERD_GRAPH_CLIENT_SECRET`, `SHEPHERD_SECURITY_ENCRYPTION_KEY`, `SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD` — populated by hand or by an operator you manage yourself. |
| `externalsecret.yaml` | Toggle `externalSecrets.enabled` (default off), mutually exclusive with both `existingSecret` and `.Values.secrets` (the chart `fail`s rather than pick a winner). Renders ESO Password generators plus an ExternalSecret so the encryption key and the bootstrap admin password are generated in-cluster and never pass through a shell or values file. `refreshInterval` MUST stay `"0"` — these are not rotatable; a re-generated encryption key orphans every already-encrypted secret in the database with no error at the moment it happens. |
| `cnpg-cluster.yaml` | Toggle `cnpg.enabled` (default off): optionally renders a CloudNativePG `Cluster` for the chart to own end-to-end. `cnpg.render: auto\|always\|never` controls a `lookup`-based skip so `helm upgrade` never re-renders (and under GitOps, never destroys) an existing Cluster; requires the CloudNativePG operator, which this chart does not install. |
| `migrate-job.yaml` | Helm hook Job (`pre-install,pre-upgrade`, `hook-delete-policy: before-hook-creation,hook-succeeded`, hook-weight `-5`) running `["migrate", "up"]` with the same image/env, reading its own `-migrate`-suffixed ConfigMap/Secret/ServiceAccount above. Toggle `migrations.job.enabled` (default `true`). When disabled, users may set `--auto-migrate` via `extraArgs` instead — document both, default to the Job. |
| `service.yaml` / `service-metrics.yaml` | `service.yaml`: ClusterIP, port 8080 → `http`. **`appProtocol`** on the port from `service.appProtocol` (default `kubernetes.io/h2c`, so Envoy/kgateway negotiates HTTP/2 cleartext to the pod for agents using the gRPC/Connect+proto path; set to `""` for NGINX Gateway Fabric, which refuses h2c upstreams — Shepherd serves HTTP/1.1 equally well). `service-metrics.yaml`: a second, always-ClusterIP Service (toggle `metrics.enabled`) exposing only the metrics port — deliberately not a port on the main Service, since `service.type` is operator-configurable and adding metrics there would publish it however the main Service is exposed. |
| `httproute.yaml` | Gateway API HTTPRoute (primary ingress mechanism, toggle `route.enabled`): `hostnames`, `parentRefs` (name/namespace/sectionName) from values. Single route for both UI/API and the agent path (`/collector.v1.CollectorService` is just a path prefix on the same server). |
| `ingress.yaml` | Classic Ingress alternative, toggle `ingress.enabled`, mutually exclusive with `route.enabled` in `values.schema.json`. |
| `serviceaccount.yaml`, `hpa.yaml`, `pdb.yaml` | Standard. HPA on CPU, toggle `autoscaling.enabled` (default off), min/max replicas + target CPU% configurable. PDB `minAvailable: 1` when replicas > 1. |
| `servicemonitor.yaml` | Toggle `metrics.serviceMonitor.enabled`; scrapes `/metrics` on the metrics port; configurable labels for Prometheus-operator selector matching. |
| `networkpolicy.yaml` | Toggle `networkPolicy.enabled` (default off — only meaningful on a CNI that enforces policies): ingress from any namespace to the API and metrics ports, egress unrestricted (Shepherd's database/IdP/git remotes are operator-supplied and not something the chart can enumerate; restrict at the CNI level for FQDN policies). |
| `deployment-simulator.yaml`, `service-simulator.yaml`, `serviceaccount-simulator.yaml`, `networkpolicy-simulator.yaml`, `secret-simulator-token.yaml` | S3 sandbox simulator (VB-1 §6.4), toggle `simulator.enabled` (**default `true` since v0.0.1** — see F5, `docs/project-status.md`). Every one of these is load-bearing and asserted by `deploy/helm/chart_test.go`: `automountServiceAccountToken: false` on the ServiceAccount (defense in depth — `internal/simsvc.Config.SATokenPath` also refuses to start if a token is mounted anyway); a bearer token on the control API from one of three sources in precedence order — `simulator.token.existingSecret`, else `externalSecrets.enabled`, else the chart's own generated Secret (`value` pins a literal, empty generates one random token reused across upgrades via `lookup`); a **default-deny egress** NetworkPolicy scoped to this Pod's own harness ports only — no cluster DNS, no rest-of-cluster, no internet, since the sandboxed Alloy is a child process of shepherd-simulator (not a sibling container), so this Pod's network boundary IS the sandbox boundary; non-root, all capabilities dropped, read-only rootfs, CPU/memory limits. What the chart cannot set: a per-Pod PID limit (no PodSpec field exists for one — it is a kubelet `podPidsLimit`/`--pod-max-pids` setting). |

**Server selectors (#234).** Every pod the chart runs (server, simulator, receiver, migration Job) shares `app.kubernetes.io/name` + `app.kubernetes.io/instance`, so a selector on that pair alone also matches the sandbox. The server pod template therefore adds `app.kubernetes.io/component: server`, and everything that means "the server pods" — `service.yaml`, `service-metrics.yaml`, `pdb.yaml`, `networkpolicy.yaml` and the simulator NetworkPolicy's ingress rule — selects on `shepherd.serverSelectorLabels` (`_helpers.tpl`). The Deployment's own `spec.selector` keeps the release-wide pair because it is immutable on a live Deployment; it still matches simulator pods, so tooling reaches a server pod through `svc/<release>` or the `component=server` label, never `deploy/<release>`.

### 17.2 `values.yaml` shape (top level)

```yaml
image: { registry: "", repository: shepherd, tag: "", pullPolicy: IfNotPresent, pullSecrets: [] }
replicas: 2
service: { ... }
config: { ... }            # mirrors §14 exactly, minus secret fields
cnpg: { enabled: false, render: auto, instances: 2, storage: { size: 10Gi }, database: shepherd, owner: shepherd, extraSpec: {} }
externalSecrets: { enabled: false, render: auto, refreshInterval: "0", encryptionKey: { length: 32 }, bootstrapAdmin: { enabled: true, length: 24, login: admin } }
existingSecret: ""
secrets: {}                # dev-only inline secrets — mutually exclusive with existingSecret and externalSecrets.enabled
migrations: { job: { enabled: true } }
route: { enabled: false, hostnames: [], parentRefs: [] }   # off by default — an HTTPRoute on a cluster without Gateway API CRDs breaks the install
ingress: { enabled: false, className: "", hosts: [], tls: [] }
metrics: { enabled: true, serviceMonitor: { enabled: false, labels: {} } }
serviceAccount: { ... }
resources: {}, nodeSelector: {}, tolerations: [], affinity: {}, priorityClassName: "", podAnnotations: {}, extraEnv: []
autoscaling: { enabled: false, minReplicas: 2, maxReplicas: 10, targetCPUUtilizationPercentage: 80 }
networkPolicy: { enabled: false }
simulator: { enabled: true, image: { ... }, replicas: 1, resources: { ... }, allowedHosts: [], token: { value: "", existingSecret: "", key: token }, networkPolicy: { extraEgress: [] } }
```

Provide `values.schema.json` validating the above (required fields, enum checks, route/ingress mutual exclusion). Provide `ci/default-values.yaml`, `ci/full-values.yaml`, `ci/ingress-values.yaml`, `ci/generated-secrets-values.yaml`; `make helm-lint` runs `helm lint` plus `helm template` against them and fails on any error. Include `NOTES.txt` printing the URL and a ready-to-paste spoke `remoteConfig:` snippet (from §1) with the chart's hostname substituted.

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
| `oidc` | `ghcr.io/navikt/mock-oauth2-server:6.0.1` (pinned in both compose files; §D.6 requires an explicit tag, never `latest`) — a mock OIDC provider with full discovery/JWKS and an interactive login form that accepts a JSON claims blob as the "username", letting each test log in with arbitrary `oid`, `email`, and `groups` claims. Shepherd's `oidc.issuer` points here. |
| `mockmsft` | Built from `e2e/mockmsft/` — one small Go server with two route groups: **Graph**: `GET /v1.0/me/transitiveMemberOf/microsoft.graph.group` (returns groups based on a header/token the suite controls; include one paginated response with `@odata.nextLink` to exercise paging) and `GET /v1.0/groups?$filter=...`; **token exchange**: a mock Entra token endpoint (SP client-credentials) and a GitHub-App installation-token endpoint; `/__fixture` parks a Gitea PAT for the suite. There is no ADO repository fake — real git comes from the `gitea` + `gitea-init` compose services, alongside `shepherd-token`, `simulator` (profile `sim`) and the `egress-canary` probe helper. |
| `shepherd-init` | The shepherd image, one-shot: waits for postgres, runs `migrate up`, then `token create --name e2e --secret <fixed>` with the dev env var set. |
| `shepherd` | The locally built image (`make docker-build` first), `depends_on: shepherd-init: service_completed_successfully`. Config via env: mock issuer, mock graph/ado base URLs, `auth.app_admin_group_ids: ["11111111-...-appadmins"]`, encryption key, `gitsync.tick: 2s`. |
| `alloy` | `grafana/alloy:v1.20.1` (`ALLOY_IMAGE`/`ALLOY_VERSION` in `deploy/versions.env` — the single pin every Dockerfile and compose file consumes; needed for `remote_config_status` reporting, never `latest` per §D.6). Command `run /etc/alloy/config.alloy --storage.path=/tmp/alloy --server.http.listen-addr=0.0.0.0:12345 --disable-reporting`. `e2e/alloy/config.alloy` contains ONLY a `remotecfg` block: url `http://shepherd:8080`, basic_auth with the fixed token, `poll_frequency = "10s"` (the enforced minimum), attributes `cluster = "e2e-cluster"`, `role = "metrics"`. |

### 18.3 Suite mechanics

- `make e2e`: `docker compose -f e2e/docker-compose.e2e.yaml up -d --build --wait` → `ginkgo --tags=e2e ./e2e` → `docker compose ... down -v` (teardown in `SynchronizedAfterSuite`; keep the stack up with `E2E_KEEP=1` for debugging).
- Login helper: performs the real BFF flow with a cookie-jar `http.Client` — `GET /auth/login`, follow to the mock's login form, POST the claims JSON (e.g. `{"oid":"u1","email":"admin@e2e","groups":["...appadmins"]}`), follow the callback, return the authenticated client. Because Shepherd calls Graph (mockmsft) for transitive groups, the suite registers each persona's groups in mockmsft first. Personas: `appAdmin`, `orgAdmin` (member of the org's admin group), `viewer` (member of an assigned group only), `nobody`.
- All waits use Gomega `Eventually` with 90s timeout / 2s polling (one full Alloy poll cycle plus slack).

### 18.4 Scenarios (each an ordered `Describe`)

1. **Registration & claiming.** After stack-up, `Eventually` the app-admin API shows cluster `e2e-cluster` unclaimed with a live `metrics` collector instance. Claim it into a freshly created org. Assert Alloy is healthy throughout (`GET alloy:12345/-/ready`).
2. **Pipeline lifecycle (the core loop).** As orgAdmin: create a destination (pointing at a dummy secret name — the pipeline for this test uses a trivial self-contained config like `prometheus.exporter.self "e2e" { }` + a `prometheus.scrape` forwarding to `prometheus.remote_write` at a black-hole URL so no secret machinery is needed); create + enable the pipeline with matcher `cluster="e2e-cluster"`, `role="metrics"`. Assert: (a) served-config endpoint shows the declare-wrapped content and a hash; (b) `Eventually` the collector detail reports `remote_config_status: APPLIED` with that hash's config; (c) Alloy's own API confirms remote components exist — `GET alloy:12345/api/v0/web/components` contains component IDs prefixed with the remotecfg module. Then update the pipeline; assert the hash changes and status returns to APPLIED. Then disable it; assert served config returns to header-only and Alloy drops the components.
3. **not_modified efficiency.** Scrape Shepherd's own `/metrics` and assert the `shepherd_getconfig_total{result="not_modified"}` counter increases across two Alloy poll cycles with no config change.
4. **Validation gate.** As orgAdmin, attempt to save a syntactically invalid pipeline → refused, with line/col diagnostics from validation; attempt to enable a pipeline that breaks the merge (duplicate declare label forged via name collision after sanitization, e.g. names `a-b` and `a_b`) → rejected listing the affected collector, and the served hash is unchanged.
5. **GitOps sync.** Create an ADO credential (mock SP) + repo link targeting the collector, seed mockmsft with a valid `.alloy` file → `Eventually` a `source=git` pipeline exists and Alloy applies the enlarged merge. Push an invalid file via `/__fixture` → sync_status becomes `error`, last good config still served (hash unchanged). Fix the file → recovers.
6. **RBAC.** viewer can GET collectors/pipelines of the assigned collector but every POST/PUT/DELETE returns 403; `nobody` gets 403/404 on org resources; an unauthenticated management call returns 401 `unauthenticated`; agent endpoint with a wrong token secret returns Connect `unauthenticated`.
7. **Status FAILED propagation.** Serve a config that passes validation but fails at runtime apply is hard to construct reliably — instead, have mockmsft… skip; simulate by asserting the plumbing: temporarily insert (via SQL through a test-only helper) a FAILED status row is NOT needed — instead assert the field round-trips using scenario 2's transitions (UNSET→APPLYING→APPLIED). `// DECISION` comment allowed here if APPLYING is never observed due to timing.

### 18.5 CI note

`make e2e` must be runnable in any Docker-capable CI runner (no privileged mode, no kind). Total wall-clock budget: under 10 minutes. Compose project name fixed (`shepherd-e2e`) so parallel local runs are refused rather than colliding.

---

## 19. Explicit non-goals for v1 (do not build)

Webhook-triggered git sync; OpAMP support; editing git-sourced pipelines in the UI; Alloy binary version matrix testing; SSO logout (front-channel); horizontal-scale coordination beyond stateless replicas + Postgres (singleflight is per-replica — acceptable).

**No longer a non-goal, SUPERSEDED:** "multi-wizard framework beyond the one wizard" — `internal/wizard`'s registry (keyed by `wizard_kind`) now backs six wizards (`appobservability`, `blackbox`, `clustermetrics`, `database`, `podlogs`, `selfmonitoring`; `docs/gateway-tier-plan.md` W8), each registering itself via `wizard.Register` exactly as v1's pluggability note anticipated.

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
    # Generated code (gen/, sqlc, alloy_gen.go) is committed and CI's
    # generated-drift job fails any PR where it no longer matches its sources,
    # so nothing is regenerated here. The SPA is the one build-time input:
    # go:embed needs internal/spa/dist populated before go build.
    - ./scripts/build-web.sh   # canonical build script per the check-build-script guard

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
- **`internal/version`**: tiny package with `Version`, `Commit`, `Date` string vars (defaults `"dev"`), printed by `shepherd version` and returned by `GET /api/version` (binary and SPA build SHAs). A `shepherd_build_info` gauge mirroring `alloy_build_info` is specified here and **scheduled** (decision 2026-09-11) — as of v0.5.0 `internal/metrics` registers no such metric.
- **Commit convention**: Conventional Commits (`feat:`, `fix:`, `chore:`, …) — required, since the changelog groups depend on it.
- **Make targets**: `make release-snapshot` = `goreleaser release --snapshot --clean --skip=publish` (must succeed locally and in CI on every milestone from milestone 1 onward — this keeps the embed/codegen hooks honest); there is no `make release` — `.github/workflows/release.yml` runs `goreleaser release --clean` on a `v*` tag push after its own `verify` job (lint, guards, tests, helm lint, image scans), refuses a tag whose version differs from `Chart.yaml`'s `appVersion` or whose chart version is already published, then attests and scans the published images (amended 2026-09-15; the original text named an Azure DevOps service connection that never existed).
- **Helm chart versioning**: the chart is NOT released by GoReleaser. Chart `version` is bumped manually per chart change; `appVersion` is set to the app tag. CI packages and pushes the chart as OCI (`helm push` to `{{ IMAGE_REGISTRY }}/charts`) in a separate pipeline step triggered by the same tag.

---

## 22. AGENTS.md — agent instruction files (create exactly these)

The repo ships instruction files for AI coding agents that will maintain it after v1. These are **always-on context** injected into every future agent turn, so they are deliberately terse: they carry only what an agent cannot cheaply discover from the code, and they reference this spec by path instead of duplicating it. Do NOT expand them with content generated from the codebase, restate linter-enforced style, or add narrative — every extra line taxes future agents' instruction budget. Ceiling (aspirational, not enforced — the root file is 100 lines, `web/AGENTS.md` 43 and `e2e/AGENTS.md` 50 after the 2026-09-15 housekeeping): root file ≤ 100 lines; subtree files ≤ 50.

Create THREE files. Root `AGENTS.md`, verbatim (substitute the real module path):

```markdown
# Shepherd

Self-hosted Grafana Alloy fleet manager. Go 1.27 backend (Connect RPC agent API + chi REST),
React 19/TS/Vite SPA embedded via go:embed, PostgreSQL 16. Spec: docs/spec.md (authoritative).

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
- `internal/store/` — sqlc output + repositories · `internal/migrations/sql/` — golang-migrate SQL
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

`web/AGENTS.md` (≤ 50 lines): commands (`pnpm dev|build|check|typecheck|test|test:ui`), the pointers an agent needs — design tokens and screen specs live in `docs/spec.md` §13 and are non-negotiable; the shared editor is `src/editor/AlloyEditor.tsx` behind `LazyAlloyEditor.tsx`; API client types in `src/api/`; server state via TanStack Query, and the visual builder's document in its one zustand+zundo store (`src/visual/store.ts`) — and rules: never introduce new color tokens or component libraries; never call `fetch` outside `src/api/`; autocomplete schema changes go in `src/editor/alloySchema.ts` with a matching test.

`e2e/AGENTS.md` (≤ 50 lines): how to run one scenario (`ginkgo --tags=e2e --focus "GitOps" ./e2e`), `E2E_KEEP=1` to keep the stack for debugging, mockmsft fixture endpoint usage, and the rule that scenarios must stay independent of each other except the documented ordered flow.

Finally: copy this specification into the repo as `docs/spec.md` so the references above resolve, and add a one-line pointer to it from the README.

---

## Amendments from Remediation Prompt v1.1 (2026-08-17)

These amendments supersede conflicting text above.

### §D.1 — §8 Stage 3 (amended)
Stage 3 assembles the merged config for every affected collector — affected = matched by the new matcher set ∪ matched by the previous enabled revision's matchers — deduplicates merged contents by hash with the collector's identity normalised to a placeholder (§8), and runs Stage 1 AND Stage 2 on each unique content (concurrency 4, budget `validate.stage3_timeout` default 30s, fail closed on timeout).

### §D.2 — §7.1 (amended)
The OIDC code flow MUST use PKCE (S256). All auth/session cookies set `Secure: true`; config `auth.insecure_cookies: true` (default false) may disable this for non-TLS local dev only. CSRF: require `X-Requested-With: XMLHttpRequest` on mutating requests.

### §D.3 — §12 (amended)
The REST paths below were removed in v0.11.0; the Connect procedures are `PipelineService.ListRevisions` and `GetRevision`.
Add under pipelines: `GET /api/orgs/{org}/pipelines/{id}/revisions` [viewer] (list only — no `contents`, `matchers`, `enabled`, or `wizard_state`); `GET /api/orgs/{org}/pipelines/{id}/revisions/{rev}` [viewer] (the full revision, including its `contents`); `POST /api/orgs/{org}/pipelines/{id}/revisions/{rev}/restore` [editor] (writes a new revision from the given one's contents+matchers(+wizard_state) — never its `enabled`: the pipeline keeps its current enabled state, which the new revision records (#377; before it, restore also copied `enabled`, and since create always stores `enabled=false` and enable/disable write no revision, restoring revision 1 switched a live pipeline off) — through the same validation gate and authorization as `PUT .../pipelines/{id}`, Stage 3 included when the pipeline is enabled; allowed on git-sourced pipelines, whose next sync overwrites it; records a `pipeline.restore` audit row).

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

### §7.2 Local Admin (optional break-glass account) — SUPERSEDED

**Superseded by local user management (migration 0015).** This section
described a single hardcoded account, configured through `auth.local_admin.*`
in the chart and gated behind a double opt-in, whose purpose was emergency
access when OIDC was unavailable. That whole shape is gone. It is recorded
here because the constraints it states — the `local:` `user_oid` convention,
the argon2id parameters, the constant-shaped failure response — either still
hold or explain why the current code looks the way it does.

What replaced it:

- **Many accounts, not one.** The `users` table (0015) holds any number of
  local accounts, each with its own app-admin flag and per-org role in
  `org_members`. `auth.local_admin.*` no longer exists in the chart; a
  deployment that still sets it fails to boot on an unknown key rather than
  silently ignoring it.
- **Not a break-glass mode.** Local accounts are a supported way to run
  Shepherd, with or without an identity provider, and they coexist with OIDC
  rather than being gated against it. The double opt-in rule and
  `allow_with_oidc` are gone with it.
- **No warning banner.** The non-dismissible amber banner shown for
  `auth_method === "local"` is removed: it warned about an emergency that a
  supported configuration is not.
- **Bootstrap instead of configuration.** The first administrator is created
  on first boot while the users table is empty, from
  `SHEPHERD_BOOTSTRAP_ADMIN_LOGIN` / `_PASSWORD`. Leaving the password unset
  creates `admin`/`admin` with `must_change_password`, which blocks every
  request except the password change itself — see
  internal/auth.RequirePasswordChange.

What carried over unchanged:

- **`user_oid` convention:** local sessions still store
  `user_oid = "local:" + login`.
- **Password hashing:** argon2id, time=1, memory=64MiB, threads=4, salt=16B,
  key=32B, standard `$argon2id$v=19$...` encoding, via `internal/auth.HashPassword`.
  **Correction:** `shepherd hash-password` does not exist — there is no such
  CLI command in `internal/cli`. Hashing happens server-side only: on
  bootstrap-admin creation, on `shepherd dev seed`'s local users, and on a
  password change through the API. There has never been a supported way to
  hand the server a pre-hashed password from outside it.
- **Endpoints:** `GET /auth/methods` (no auth, no CSRF,
  `{"oidc":bool,"local_admin":bool}`, `Cache-Control: max-age=60`) and
  `POST /api/auth/local/login` (through CSRFMiddleware, constant-shaped
  failure on either wrong username or wrong password — separate answers would
  be a username oracle). `local_admin` in the methods response now means
  "local sign-in is available", not "the break-glass account is enabled".
- **Sessions** still carry `source = "local"`, and now also `user_id`
  referencing the `users` row (0015) — which is what authorization branches on.
- **Actor wiring** and the `/api/me` `auth_method` field are unchanged.
- **Security:** argon2id cost, constant-time compare, constant-shaped
  failure. `Authenticate` additionally verifies a dummy hash when no user
  matches, so a missing account and a wrong password take the same time.
  `POST /api/auth/local/login` is additionally rate-limited (W3-2, D4):
  two in-process `golang.org/x/time/rate` buckets, one per submitted login
  and one per source IP, each with idle eviction; a throttled request gets
  `429` with `Retry-After`. The source IP is the direct TCP peer
  (`r.RemoteAddr`), or — only when that peer is inside
  `server.trusted_proxies` (CIDRs, default empty) — the rightmost
  `X-Forwarded-For` entry not itself trusted. `X-Real-IP` and
  `True-Client-IP` are never read, and no middleware rewrites the peer
  address. With the list empty, every client behind an ingress shares one
  per-IP bucket.

### §D.9 v1.3 — Sessions schema (amended)
Sessions table gains `source text NOT NULL DEFAULT 'oidc'`. `id_token_expires` is now nullable (local sessions have no ID token). Migration 0003. Both `GetSessionByID` and `CreateSession` include the `source` column.

## Amendments from FS-1 Full-Stack Integration Tests + Local Dev Stack (2026-08-17)

### §12 (amended) — /api/me canonical contract

The REST path was removed in v0.11.0; the same contract is `MeService.GetMe` (an unauthenticated call answers Connect `unauthenticated`).

**Unauthenticated:** `401 {"error":{"code":"unauthenticated","message":"not authenticated"}}`

**Authenticated:**
```json
{
  "user_oid": "string",
  "email": "string",
  "display_name": "string",
  "is_app_admin": boolean,
  "auth_method": "oidc"|"local"|"dev",
  "orgs": [{ "id": "uuid", "name": "string", "display_name": "string", "role": "ROLE_ADMIN"|"ROLE_EDITOR"|"ROLE_VIEWER" }]
}
```

`orgs` lists orgs the session user has access to. For `is_app_admin=true`: all orgs with `role=ROLE_ADMIN`.
For a non-app-admin OIDC session: orgs where `group_ids` match `admin_group_id`
(`ROLE_ADMIN`), `editor_group_id` (`ROLE_EDITOR`) or `viewer_group_id` (`ROLE_VIEWER`).
For a local session: the user's `org_members` rows, whose `role` is one of
those three (and `ROLE_VIEWER` for a team-only member). Always an array, never null. The reported value drives what the
UI offers, so it names the role actually held — reporting viewer to an
editor would hide the pipeline editor from someone who can in fact use it.

### §12 (amended) — GET /api/admin/clusters canonical contract

The REST path was removed in v0.11.0; the same contract is `AdminService.ListClusters` (with its `unclaimed` filter).

Returns ALL clusters (claimed + unclaimed). Shape: `{id, name, org_id (empty UUID if unclaimed), created_at}`.
Use `?unclaimed=true` to filter to unclaimed only. The old behaviour of returning only unclaimed clusters by default is a bug (fixed in FS-1).

### §14 (amended) — Dev tooling

`shepherd dev seed` and `shepherd dev create-session` are developer-only CLI commands:
- Direct DB access — no HTTP flow, no auth required.
- `dev seed`: idempotent, ON CONFLICT DO NOTHING, creates 2 orgs, 3 clusters (`prod-eu-1`, `staging-eu-1`, `data-eng-eu-1`), collectors, pipelines and a token.
- `dev create-session`: inserts session row with `source='dev'`, prints session ID to stdout.
- Both commands must NEVER be exposed via HTTP and are not to be called in production.
- `shepherd dev create-session --persona` supports: `appadmin`, `admin-platform`, `viewer-platform`, `nobody`.

### §15 (amended) — Test suite layers + CI ordering

Three Playwright layers:
1. **Mocked suite** (`make test-ui`, `playwright.config.ts`): full network mock at API boundary. Scope: UI behaviour, component contracts, loading/error states, mock-state flows. No real backend.
2. **Fullstack suite** (`make test-fullstack`, `playwright.fullstack.config.ts`): REAL backend at `:8080`, NO `page.route()` interception. Scope: UI-API contract correctness, RBAC enforcement, real CRUD persistence, real recompute path. `workers: 1`, fresh DB per suite.
3. **E2E suite** (`make e2e`): Alloy agent protocol (remotecfg polling, GetConfig RPC, hash/not-modified).

**CI ordering:**
```
PR:      [lint ∥ build ∥ guards ∥ generated-drift ∥ test ∥ web ∥ test-ui ∥ test-fullstack]
push main: same set, path-filtered (see below) → e2e (D11, path-filtered on e2e/agentapi/gitsync/auth/Dockerfile/versions.env)
merge_group / workflow_dispatch: → e2e (currently dormant — this repo has no merge queue configured)
```
`e2e.yml` is no longer merge-queue-only as originally specified: it also runs post-merge on every qualifying push to `main`, path-filtered to what the suite actually exercises, because `merge_group` never fires without a configured merge queue and the agent-protocol suite otherwise never ran at all (D11). `workflow_dispatch` remains available on demand.

One documented exception: `.github/workflows/e2e.yml`'s `e2e-egress` job (`make e2e-sim`) also
runs on any PR touching the S3 sandbox containment surface. Its first ginkgo pass is the egress
probes under `--fail-on-empty`: those probes are the ONLY control that bounds what a sandbox run
can reach — the transform bounds credentials, not reachability — so reviewing a change to the sim
network, the simulator or the transform without having exercised them is reviewing the claim
rather than the control. Its second pass runs the S3 run-lifecycle specs, so the same job also
proves the sandbox DELIVERS (a run reaching `completed` with captured series) and not only that it
contains. See VB-1 §6.4, `docs/proofs/simulator-containment.md` §P0 and
`docs/proofs/sandbox-sim-e2e.md`.

`test-fullstack` runs on every PR, parallel with `test-ui`, gated by the `changes` job: skipped when the diff is confined to `e2e/k8s/`, `deploy/helm/` or documentation (Markdown, `docs/`, `site/`, `scripts/docs-content/`). Compose logs artifact on failure to `/tmp/fullstack-stack.log`.

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
  e2e-egress paths-filtered on PRs touching the sandbox containment surface; e2e runs post-merge
  on push to main, path-filtered (D11), plus on `merge_group`/`workflow_dispatch`; e2e-k8s and
  schema-verify both run weekly (not nightly — cron budget, see `docs/project-status.md` §6).
  `make smoke` is wired into CI's `test-fullstack` job (S20) — it shares that job's already-built
  images, so it costs only the ~30-60s smoke run itself.
