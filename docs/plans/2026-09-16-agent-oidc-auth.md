# Collector OIDC authentication — implementation plan (2026-09-16)

Lets an Alloy collector authenticate to Shepherd with an OIDC/OAuth2 access
token instead of the shared agent-token Basic credential. The token is
**claim-scoped**: it names the org (and optionally the clusters and roles) the
collector may act for, so a token minted for one org cannot pull another org's
config even if it names that org's cluster.

Every root cause and seam below was read in the code before the design was
fixed; file:line references are to `main` at 4428679. This plan is the
ask-first record (per `AGENTS.md`) for the proto, RBAC-semantics, and
served-config changes it proposes — no dependency is added (`go-oidc` is
already vendored).

## 0. Why this is small on the agent and lands on one seam

Alloy's `remotecfg` block already speaks every part of the client side. It
supports an `oauth2` sub-block (`client_id`, `client_secret` /
`client_secret_file`, `token_url`, `scopes`) — the OAuth2 client-credentials
grant — as well as `bearer_token` / `bearer_token_file` and a custom
`authorization` header, all mutually exclusive
(https://grafana.com/docs/alloy/latest/reference/config-blocks/remotecfg/).
So the collector obtains and refreshes the token itself and presents it as
`Authorization: Bearer <jwt>` on each poll. **No agent-side code.**

On Shepherd's side the collector API is one Connect service
(`internal/agentapi`, `CollectorService`) behind a single request gate,
`NewAuthGate` (`internal/agentapi/auth.go:30`), which today reads only
`Authorization: Basic`. The whole feature is: teach that gate a `Bearer`
branch, verify the JWT with the OIDC provider Shepherd already builds for user
login, and enforce the claim scope before config is served. The
service-account gate (`internal/mgmtapi/machine_auth.go:98`) already
demonstrates a one-gate-two-credential-shapes pattern to mirror.

## 1. Section-1 contract (names other slices depend on)

- **`auth.AgentClaims`** — `{ Issuer, Subject, AppID, Org string; Clusters,
  Roles []string }`, the validated token. `AppID` is the value of the
  configured app-identity claim (default `sub`; may be `azp`/`client_id`),
  `Org`/`Clusters`/`Roles` the optional scope claims (empty = unset).
- **`auth.Handler.VerifyAgentToken(ctx, rawJWT) (AgentClaims, error)`** — the
  new exported verifier. Returns a typed error for: not-a-JWT, bad signature,
  wrong issuer, `aud` not the agent audience, expired. It does **not** require
  an org claim — org is resolved separately (§3.3), because a shared OIDC app
  cannot carry a per-instance org.
- **`agentapi.Principal`** — carried in the request context after the gate:
  `{ Kind AuthKind; TokenID string; Claims *auth.AgentClaims }`, `AuthKind ∈
  {agentToken, agentOIDC}`. Replaces the bare `tokenIDKey` value
  (`auth.go:36`), which nothing reads today anyway.
- **`agent_identities` table** — the admin-configured binding for the
  registration path: `(issuer, app_id) → org_id` plus optional `clusters` /
  `roles` allowlists, `created_by`, timestamps. The local counterpart to a
  token claim.
- **Config keys** (chart + UI): `oidc.agent_audience`, `oidc.agent_app_claim`
  (default `sub`), `oidc.agent_org_claim` (default `shepherd_org`),
  `oidc.agent_clusters_claim` (default `shepherd_clusters`),
  `oidc.agent_roles_claim` (default `shepherd_roles`), and
  `oidc.agent_trust_org_claim` (bool, default false — see §3.3). Agent OIDC is
  **on** iff `agent_audience != ""`.
- **`beacon` env names** for the OAuth2 write-back:
  `SHEPHERD_OIDC_CLIENT_ID`, `SHEPHERD_OIDC_CLIENT_SECRET`,
  `SHEPHERD_OIDC_TOKEN_URL` (alongside the existing `SHEPHERD_AGENT_TOKEN_*`).

## 2. Signed decisions

- **D1 — Scoped identity, resolved by a chain, not by a single mechanism.**
  An authenticated OIDC collector's org is resolved in order (§3.3): (1) an
  admin-configured `agent_identities` binding keyed on the token's `(issuer,
  app_id)`; (2) the token's own `shepherd_org` claim, but only when
  `agent_trust_org_claim` is on (the IdP is then authoritative for org
  assignment); (3) otherwise the existing admin cluster-claim, with the OIDC
  token proving only liveness. This chain — rather than "org claim, always" —
  is what lets one design cover both a per-instance-app fleet and a
  shared-app fleet (see §2.1). Where org is resolved by (1) or (2), Shepherd
  also enforces `cluster ∈ Clusters` and `role ∈ Roles` when those are set.
- **D1a — Auto-claim (consequence of D1, called out because it changes a
  served-config invariant).** Today an unclaimed cluster is served empty until
  an app-admin runs `ClaimCluster` (`internal/agentapi/service.go:178`). For a
  claim-scoped OIDC collector the org is proven by the token, so on the first
  authenticated poll Shepherd binds the cluster to the token's org **iff the
  cluster is unclaimed**. If the cluster is already claimed by a *different*
  org, the request is refused (`PermissionDenied`), never silently reassigned.
  A cluster claimed by admin and later polled by a matching-org token is
  served normally. This removes the manual claim step for OIDC fleets, which
  is the point of claim-scoping; it is the one new RBAC rule and is the thing
  to review hardest.
- **D2 — Reuse the SSO issuer, add an audience.** Same IdP, same discovery and
  JWKS. A new `agent_audience` distinguishes an agent access token from a user
  login ID token. The agent verifier requires `aud == agent_audience` and so
  can never accept a login token (whose `aud` is the login `client_id`), and
  vice-versa. One issuer to trust and rotate.
- **D3 — Beacon uses the same token.** The rendered beacon
  `prometheus.remote_write` block gets an `oauth2` stanza (reading client
  credentials from `sys.env`), and the beacon ingest endpoint learns the same
  `Bearer` branch. One credential end to end.
- **D4 — Additive.** Basic agent tokens keep working unchanged; the gate
  branches on the `Authorization` scheme. Nothing forces a migration.

## 2.1 Deployment scenarios this must cover

The governing rule: **an instance can be auto-assigned to an org only if it
presents something distinct-per-org that Shepherd can trust** — a token claim,
or the token's own identity (`sub` / `azp` / `client_id`). A credential that is
identical across instances carries no per-org signal, so those instances
cannot self-assign; an admin must assign them, or they must be given distinct
credentials.

| Scenario | How org is resolved | Chain step |
|---|---|---|
| **One OIDC app for the whole fleet, admin assigns each Alloy's cluster to an org** | The shared app proves liveness; the admin runs the existing cluster-claim per cluster. Supported. | (3) |
| **One OIDC app, instances "self-assign" from token metadata** | **Not securely supported.** A shared client-credentials app issues an identical token to every instance, so there is no trustworthy per-instance org signal — the only per-instance data is the request-body `attributes`, which are spoofable and must not decide org. To self-assign, give each instance a distinct credential (which is the next scenario). | — |
| **A distinct OIDC app per org (or per instance), IdP emits a `shepherd_org` claim** | Shepherd reads the org from the claim, with `agent_trust_org_claim` on. Fully auto; no Shepherd-side mapping. | (2) |
| **A distinct OIDC app per org (or per instance), admin maps app → org in Shepherd** | Admin creates an `agent_identities` binding `(issuer, client_id) → org`; the claim can stay off. Fully auto; the mapping lives in Shepherd, not the IdP. | (1) |

So your first scenario is supported through the admin cluster-claim (step 3);
its "self-assign from a shared app" variant is the one thing that cannot be
done securely, and the plan says so rather than pretending otherwise. Your
second scenario — unique app per instance, auto-assigned — is supported two
ways: by an IdP-emitted claim (step 2) or by an admin-configured mapping (step
1), and a deployment may mix all three across different fleets.

## 3. The seam, in code

### 3.1 Verifier (`internal/auth`)

The primitive already exists as a local variable in the login callback —
`rt.provider.Verifier(&oidc.Config{ClientID: rt.settings.ClientID})`
(`auth.go:440`). Expose an agent variant on the runtime:

- Add `agentAudience`, the three claim names, and a built
  `agentVerifier *oidc.IDTokenVerifier` to `oidcRuntime` (`auth.go:96`),
  constructed in `Reload` (`auth.go:211`) when `agent_audience` is set:
  `rt.provider.Verifier(&oidc.Config{ClientID: agentAudience})`.
- `VerifyAgentToken(ctx, raw)`: `refreshIfStale`, load the runtime, verify,
  then decode claims into `AgentClaims` using the configured claim names
  (mirroring `resolveGroups`, `auth.go:520`, for the list-or-string groups
  shape). Reject when the org claim is absent — claim-scoping requires it.
- Same source-based client split as user discovery
  (`discoveryClientFor`, `discovery.go:182`): chart issuer unguarded,
  UI issuer guarded by `dialGuard`. No new SSRF surface.

### 3.2 Gate (`internal/agentapi/auth.go`)

```
func NewAuthGate(st *store.Store, verifyAgent BearerVerifier) connect.RequestGateFunc
```

Branch on scheme:
- `Bearer ` → `claims, err := verifyAgent(ctx, token)`; on success put a
  `Principal{Kind: agentOIDC, Claims: &claims}` in ctx. If OIDC agent auth is
  off (`verifyAgent == nil`), a `Bearer` header is a 401 — the feature is
  simply not enabled.
- `Basic ` → the existing `verifyBasicAuth` path, wrapped as
  `Principal{Kind: agentToken, TokenID: id}`.
- neither → `CodeUnauthenticated`.

`verifyAgent` is `authHandler.VerifyAgentToken`, injected at
`internal/server/server.go:269` where the gate is mounted.

### 3.3 Enforcement (`internal/agentapi/service.go`)

`requireClusterRole` still reads `cluster`/`role` from the body
(`service.go:351`). After it, when the principal is `agentOIDC`, resolve the
org through the D1 chain:
1. `GetAgentIdentity(issuer, app_id)` → if a binding exists, its org (and its
   optional cluster/role allowlists) win;
2. else, if `agent_trust_org_claim` and `claims.Org != ""`, resolve that org
   by its external id (a new `GetOrgByExternalID` query);
3. else, no OIDC-derived org — fall through to today's `GetCollectorOrgID`
   (admin cluster-claim); an unclaimed cluster still serves empty.

When org came from (1) or (2):
- if the resolved binding/claim carries a cluster allowlist, require `cluster
  ∈ allowlist`, else `PermissionDenied`; likewise `role`;
- **auto-claim (D1a):** if the cluster row is unclaimed, `ClaimCluster` to the
  resolved org; if claimed by another org, refuse. A write on the poll path,
  guarded by a trusted per-instance identity — reuses `ClaimCluster`
  (`clusters.sql:19`). Not reached in mode (3), where the admin claims.

The rest of `GetConfig` (serve cache, recompute) is unchanged: once org is
resolved the served config is computed exactly as for a Basic-auth collector.

### 3.4 Beacon (`internal/agentapi/beacon_handler.go`, `internal/beacon/render.go`)

- Ingest: `beacon_handler.go:73` gains the same `Bearer` branch. The identity
  key for rate-limiting and the `beacon_components` upsert
  (`beacon_handler.go:83,131`) is the token id today; generalise it to a
  `principal` string — `sub` for OIDC, token id for Basic. This is a store
  change (a text column rename/add) and a migration.
- Render: when agent OIDC is the configured mode, `render.go:168` emits an
  `oauth2 { client_id = sys.env("SHEPHERD_OIDC_CLIENT_ID"); client_secret =
  sys.env("SHEPHERD_OIDC_CLIENT_SECRET"); token_url = "<issuer token
  endpoint>"; scopes = [...] }` block instead of `basic_auth`. The client
  credentials stay out of the shared serve-cache exactly as the token env
  vars do today (`render.go:31`).

### 3.5 Config, proto, chart, UI

- `internal/config/config.go OIDCConfig` (`config.go:69`): add
  `AgentAudience`, `AgentAppClaim`, `AgentOrgClaim`, `AgentClustersClaim`,
  `AgentRolesClaim`, `AgentTrustOrgClaim bool`.
- `proto/shepherd/mgmt/v1/admin.proto OidcSettings` (`admin.proto:219`, next
  free field 23): add `agent_audience = 23`, `agent_app_claim = 24`,
  `agent_org_claim = 25`, `agent_clusters_claim = 26`, `agent_roles_claim =
  27`, `agent_trust_org_claim = 28`, read-only `agent_auth_enabled = 29`.
  Plus a small `AgentIdentityService` (or additions to `AdminService`) for the
  `agent_identities` bindings: `List/Create/DeleteAgentIdentity`, app-admin
  only. **Proto change — ask-first; this plan is the record.** `buf generate`
  regenerates Go + TS.
- `deploy/helm/shepherd/values.yaml` oidc block (`values.yaml:107`): the new
  keys, claim-name defaults, `agentAudience` empty and `agentTrustOrgClaim`
  false (feature off by default). **Chart template change → chart version bump
  → release.**
- Admin UI: agent-audience and claim fields on `AdminAuthPage` (read-only when
  chart-owned, as the rest of the SSO form); a small bindings table (app id →
  org, cluster/role allowlists) for the registration path — the local
  counterpart to `/admin/tokens`.

## 4. Onboarding and docs

- `deploy/helm/shepherd/templates/NOTES.txt:48` — add the `oauth2` remotecfg
  variant beside the `basic_auth` one.
- `internal/chartvalues/render.go:110` — a `type: oauth2` auth mode for the
  k8s-monitoring values generator.
- `web/src/pages/AdminTokensPage.tsx` / `AdminAuthPage` — surface the audience
  and the token endpoint the operator points Alloy at.
- Docs site: a "Collector OIDC auth" section on `single-sign-on.html` and
  `collectors.html`, and the claim contract (`shepherd_org`, etc.) with a
  worked example per common IdP (Keycloak client scope, Entra app role). This
  extends the agent-token docs the 2026-09-15 audit already flagged.

## 5. Tests and live proof (red-first)

- **Verifier unit** (`internal/auth`): a self-signed test issuer (an in-test
  JWKS + `oidc` verifier, or the `mock-oauth2-server` already in `dev/`/`e2e`)
  — accepts a valid agent token, rejects wrong audience, expired, bad
  signature, missing org claim. Red first.
- **Gate unit** (`internal/agentapi`): `Bearer` valid → principal in ctx;
  `Bearer` when disabled → 401; `Basic` still works; malformed → 401.
- **Resolution-chain unit** (`internal/agentapi`, integration with the
  testcontainer Postgres), one per D1 mode: (1) an `agent_identities` binding
  resolves org and its allowlists; (2) with `agent_trust_org_claim` on, the
  `shepherd_org` claim resolves org, and with it **off** the claim is ignored
  and mode (3) applies; (3) no binding/claim → today's admin cluster-claim,
  unclaimed serves empty. Plus: cluster ∉ allowlist refused, unclaimed cluster
  auto-claimed, cluster claimed by another org refused, role constraint
  enforced. Binding wins over claim when both are present.
- **Beacon unit**: `Bearer` write authenticated, keyed by `sub`.
- **Chart**: `helm template` renders the four values; `config-scan` /
  repocheck cover the NOTES.txt stanza.
- **Live (e2e/k8s, the real proof):** the kind dev stack already runs
  `mock-oauth2-server` (`dev/kind/oidc.yaml`) and Alloy agents. Add an agent
  configured with a `remotecfg { oauth2 { ... } }` block whose token carries a
  `shepherd_org` claim, and assert it registers, is auto-claimed to that org,
  and is served that org's config — driven from the existing OIDC walk script.
  A negative agent (token for org B naming org A's cluster) must be refused.

## 6. Phasing (one PR per phase, each green before the next)

1. **Verify + gate + enforce + config/proto**, config polling only; beacon
   left on Basic. Unit + integration tests. No chart change yet (config via
   `extraEnv`), so no release pressure.
2. **Beacon OIDC** — ingest `Bearer` branch, `principal` key migration,
   `render.go` `oauth2` stanza.
3. **Chart values + UI + onboarding stanzas + docs**, then the e2e/k8s live
   proof. Chart bump.
4. **Release** (chart minor, appVersion bump) per
   `docs/../shepherd-release-conventions`.

## 7. Risks and non-goals

- **Audience is load-bearing.** If `agent_audience` were ever empty-but-enabled
  the verifier would accept any token the issuer signs; the code must treat
  empty audience as "feature off", never "accept all". A repocheck/unit guard
  pins this.
- **Opaque access tokens.** Some IdPs (Okta org server, Entra v1) issue
  non-JWT access tokens the verifier cannot parse. Documented as a
  prerequisite: the IdP must issue JWT access tokens with the configured
  audience. Out of scope to support introspection endpoints in v1.
- **Auto-claim trust (D1a).** Only a validly-signed token resolved to an org by
  a binding or a trusted claim can bind a cluster, and only within that org;
  cross-org naming is refused. The central new authority and the reason this
  plan is the ask-first record.
- **Trusting the org claim (mode 2) hands org assignment to the IdP.** Off by
  default (`agent_trust_org_claim`): a compromised or loose IdP could then mint
  a token for any org. The registration path (mode 1) keeps assignment in
  Shepherd for operators who do not want the IdP to be authoritative.
- **A single shared OIDC app cannot self-assign instances to different orgs.**
  Documented as a topology constraint (§2.1), not a bug: such fleets use the
  admin cluster-claim, or move to a per-instance credential.
- **Not** replacing agent tokens, **not** adding a machine-identity provider
  (D2 reuses the SSO issuer), **not** supporting token introspection.

🤖 Generated with [Claude Code](https://claude.com/claude-code)

https://claude.ai/code/session_01BPMsuQQBnedAsJ8tJMH3rw
