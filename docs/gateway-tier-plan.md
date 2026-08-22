# Gateway tier, beacon, and tenant routing — multi-session implementation plan

> Status: **all eleven workstreams built as of 2026-08-22. W1, W2 and W3 are done; W4-W11 are built
> and awaiting their review gates — R1, R2, R3, R5, R6 all remain unsigned, and nothing here is
> user-reachable until they are.** This document is the execution plan and
> per-step ledger for the work; `docs/project-status.md` remains the single live status
> document for the product as a whole and links here. Do not start a second product ledger:
> step status lives in §9 of this file, product status lives there.

## 1. Why this exists

Two deployment scenarios pull Shepherd in different directions, and the difference decides
what we build:

- **BYO Alloy.** An operator deploys Alloy themselves (usually via Grafana's k8s-monitoring
  chart) and points `remotecfg` at Shepherd. Shepherd is a config server. It knows only what
  the agent reports: `id`, `name`, `local_attributes`, `hash` (`proto/collector/v1`). It has
  **no visibility** into what that agent already collects or where it already ships —
  remotecfg is pull-only and never uploads local config.
- **Shepherd-managed.** Shepherd is the hub for many clusters' collectors. The temptation is
  to have Shepherd deploy and reconcile Alloy in customer clusters; see D4 for why we do not.

Both scenarios converge on one keystone: **a tenant-aware gateway tier**. It is what makes
browser (Faro) and OTLP ingest work without client-side tenant configuration, and it is what
makes serverless onboarding a matter of handing out an endpoint rather than generating
collector configs.

## 2. Decisions

These are settled. Revisit them in a new amendment, not by quiet drift.

### D1 — Gateway API only; no Ingress path

Shepherd renders `HTTPRoute` and nothing else. There is no Ingress fallback and no
annotation-based abstraction layer. One renderer, portable route semantics, no
lowest-common-denominator behaviour.

`GATEWAY_API_VERSION` is pinned in `deploy/versions.env` (baseline: the 1.4 line — most
controllers support it), guarded for consistency the way image pins already are
(`make check-docker` pattern), and **the Standard channel is the contract**. The channel
matters more than the number: Experimental-channel features can change shape between
releases.

### D2 — Conformance requirements, and no hard dependency on the CORS filter

Shepherd depends on exactly two filter behaviours, at these conformance levels:

| Need | Filter | Level (v1.4) | In Standard channel? | Used for |
|---|---|---|---|---|
| Tenant identity | `RequestHeaderModifier` | **Core** | yes | inject `X-Scope-OrgID` downstream so clients never set it |
| Prefix routing | `URLRewrite` (`ReplacePrefixMatch`) | **Extended** | yes | `/otlp/{tenant}/v1/traces` → `/v1/traces` at the backend |
| CORS | `CORS` filter | Extended | **NO at 1.4** | *not used* — see below |

**CORS is handled by Alloy, not the gateway**, and this is now verified rather than assumed.
Checked on 2026-08-21 against the released CRDs themselves: v1.4.0's **Standard channel contains
no CORS filter at all** (it exists only in the Experimental channel), while `URLRewrite` and
`ReplacePrefixMatch` are both present. CORS reaches the Standard channel in **v1.6.1**. Since the
floor is the 1.4 line, depending on the gateway's CORS filter would have been a hard dependency on
an Experimental-channel feature — exactly what D2 forbids. `faro.receiver`'s `cors_allowed_origins`
answers CORS instead; the gateway routes and injects tenant. Revisit only if the supported floor
ever rises past 1.6.

Support levels come from `apis/v1/httproute_types.go` at tag `v1.4.0`; channel membership from
diffing the released `standard-install.yaml` against `experimental-install.yaml`.

### D3 — Detect the CRD version at runtime and refuse clearly

Shepherd reads the installed Gateway API CRD version and refuses with a sentence naming what
it found and what it needs, rather than emitting `HTTPRoute`s a controller silently ignores.
A route that renders but routes nothing is exactly the silent-by-construction failure class
`docs/project-status.md` §6 names.

### D4 — GitOps is the delivery mechanism for customer clusters

Shepherd does **not** hold customer cluster credentials and does not imperatively deploy or
reconcile Alloy there. It generates the deployment artifact (chart values / manifests),
commits it through the existing GitOps machinery (repo links, credentials, revisions, audit),
and the customer's Flux/Argo applies it. Rebuilding Argo inside Shepherd buys a worse version
of something the customer already runs, plus the blast radius of cluster-admin credentials.

The exception, and the only infrastructure Shepherd manages directly: **the receiver/gateway
tier in the observability cluster we already own** (D5, W4).

### D5 — No OpenTelemetry Collector renderer

The schema artifact, renderer, and visual builder are Alloy-shaped. Serverless and SDK
clients are served by **an endpoint plus onboarding artifacts**, not by generating collector
configs: a Lambda needs `OTEL_EXPORTER_OTLP_ENDPOINT`/`_HEADERS` and a layer, not a pipeline.
Revisit only if a customer needs per-function pipeline logic the gateway cannot express.

Corollary: onboarding artifacts must not hardcode ADOT layer ARNs (region- and
arch-specific, drift constantly) unless they are pinned and freshness-checked like
`ALLOY_VERSION`. First implementation links AWS's published list and fills in the env vars.

### D6 — The beacon is baseline-served, and carries inventory, not config

Every collector is served a small baseline pipeline (`prometheus.exporter.self` →
`prometheus.relabel` → `prometheus.remote_write` to Shepherd). It is **not opt-in**: the
collector we know nothing about is precisely the one that would never opt in.

- Auth is free: agents already authenticate with HTTP Basic (`uuid:secret`,
  `internal/agentapi/auth.go`) and Alloy's `remote_write` speaks basic auth.
- It carries **component names and health, never config text** — no secret ever crosses the
  wire, which is a materially easier security conversation than "upload your config".
- Shepherd is not a TSDB: the ingest endpoint parses, projects to inventory + health, and
  discards. Body-size capped, and rate-limited per **authenticated agent token** — not per
  instance, as this line originally said. The token is the only identity the ingest layer can
  establish cheaply and trustworthily per request; a fleet sharing one token shares one bucket.
  At the default 2 rps that starts shedding beacons somewhere above ~120 collectors on a single
  token, which is a real scale limit an operator should know about rather than discover.

### D7 — Grafana integration is optional enrichment, scoped to verification

An optional Grafana service-account token lets Shepherd answer the question its story currently
stops short of: *did the data actually arrive?* Querying the destination (`/api/ds/query`) after a
pipeline ships is the same principle as this repo's existing "`alloy validate` is not `alloy run`"
standard — assert the observable consequence — extended past the collector into production.

Bounded deliberately: **verification first**, destination import (endpoints and types; Grafana will
not hand back datasource secrets) if asked, deep links into Explore *which need no token at all* and
should exist regardless, and **no dashboard or alert-rule management**. Minimum token scope,
documented. Shepherd must keep working with no Grafana configured.

### D8 — Both gateway ownership modes are supported; the operator chooses

Shepherd can either **manage its own Gateway** (it creates and owns the `Gateway` object in the
receiver tier's namespace) or **bind to a Gateway the operator already runs** (Shepherd only
creates `HTTPRoute`s with a `parentRef` at it). Neither is privileged; the choice is per
installation.

The renderer needs no branch for this: it emits a `ParentReference` by name and namespace and is
indifferent to who created the target. What actually differs:

| | Shepherd-managed | Operator-owned |
|---|---|---|
| Who creates the `Gateway` | Shepherd | the operator, out of band |
| Hostname / TLS | Shepherd's config | inherited from the existing Gateway's listeners |
| RBAC Shepherd needs | create/update on `Gateway` **and** `HTTPRoute` in its namespace | create/update on `HTTPRoute` only, in the Gateway's namespace (or a namespace its `AllowedRoutes` admits) |
| Failure mode to surface | none extra | the target Gateway may not exist, may not admit routes from this namespace, or may have no listener the route can attach to — all must be reported as clear refusals, not silent non-routing |

The operator-owned mode is the one that needs care: `AllowedRoutes` on the Gateway's listeners
governs whether our `HTTPRoute` is admitted at all, and a rejected route is exactly the
"renders but does not route" failure D3 exists to prevent. Attachment must be verified
(`Accepted=True` on the route's parent status), not assumed.

### D9 — Two route-prefix formats, operator's choice

The prefix is an **identifier, not an authorizer** (§3). Two supported formats, because the
trade-off is a real product preference and not something to decide for everyone:

| Format | Example URL | Why choose it |
|---|---|---|
| **Opaque** | `https://telemetry.example.com/otlp/k7f3n9qp/v1/traces` | discloses nothing — no tenant name in URLs, logs, Referer headers or browser history. For operators who treat their customer list as sensitive |
| **Slug + suffix** | `https://telemetry.example.com/otlp/acme-a1b2c3/v1/traces` | debuggable *and* unguessable: ops reads `acme-…` in a gateway log and knows whose traffic it is, while the suffix stays unguessable |

**Default: slug + suffix.** The only thing it discloses is the tenant's own name, to someone who
already holds the URL — while the ops benefit (reading `acme-…` in a gateway log mid-incident)
is real and recurring. Operators who treat their customer list as sensitive set the opaque format
instead; the setting is per installation, and switching it affects only newly issued segments,
since existing ones stay valid until rotated.

Both are rotatable (issue new, run both briefly, revoke old) and both are opaque to the
renderer, which already takes the segment as a value — so this is a generation policy, not a
rendering concern. Whichever is chosen, generation must enforce: a bounded charset (no path
separators, no control characters, no percent-encoding), a length bound, and uniqueness.

**Signed prefixes are explicitly rejected**: Gateway API cannot verify a signature —
`RequestHeaderModifier` and `URLRewrite` compute nothing — so verification would need the
receiver or the Experimental-channel `ExternalAuth` filter, which D2 forbids.

**Subdomain-per-tenant** (`https://acme-a1b2c3.telemetry.example.com/v1/traces`) is a documented
future variant, not built: it removes path rewriting entirely and allows per-tenant certificates,
but requires wildcard DNS and a wildcard certificate, which an operator-owned gateway may not
have. The renderer change is small (hostname match instead of path match) if demand appears.

**Compatibility note that shapes W7**: OTLP SDKs append `/v1/traces`, `/v1/metrics`, `/v1/logs`
to `OTEL_EXPORTER_OTLP_ENDPOINT`, so a client is configured with only the base
(`https://telemetry.example.com/otlp/<segment>`) and the SDK builds the rest. That holds for both
formats, which is what makes the onboarding artifact a single environment variable.

### D10 — OTLP is the first-class frontend path; Faro is demand-driven

Researched against the pinned Alloy schema and the Faro SDK's own source, not from
recollection.

**Faro is OpenTelemetry, for traces.** `@grafana/faro-web-tracing` depends on
`@opentelemetry/sdk-trace-web`, the fetch/XHR instrumentations, and
`@opentelemetry/exporter-trace-otlp-http` — it already speaks OTLP. What Faro adds beyond OTel
is the RUM batteries in its own payload format: uncaught error capture, console logs, Web
Vitals, session tracking, plus the receiver's `sourcemaps` block for unminifying stack traces.
Only that half needs `faro.receiver`.

**The two paths have very different tenancy properties**, and this is the decision driver:

| | OTLP | Faro (RUM payload) |
|---|---|---|
| Trustworthy tenant on ONE port | **yes** — `otelcol.receiver.otlp` has `include_metadata`, and `otelcol.auth.headers` has `from_context`, so the gateway-injected header propagates | **no** |
| Where a tenant value can come from | gateway-set header (client cannot spoof — G3/G4) | the **client-supplied payload** (`extra_log_labels = {"app" = ""}` reads from the payload; see `exporters.go`'s `labelSet`) |
| Scaling | one listener, N tenants | one listener **per tenant**, or an instance per tenant |

`include_metadata` on `faro.receiver` propagates connection metadata to *otelcol* consumers
(the traces output) — it does **not** put HTTP headers onto Loki labels, so the logs path
cannot see the gateway's header. And the gateway cannot substitute path for port here:
`faro.receiver` serves a fixed path, so every tenant prefix rewrites onto the same one.

**Therefore: OTLP first.** It is vendor-neutral, carries the correlation-critical signal, and
scales on a single port with the stronger isolation guarantee. Faro support enters when a user
asks for the RUM batteries — the same demand-driven rule as W9 — not speculatively.

**Documented hybrid** that halves the problem for teams who want both: use the Faro SDK for RUM
batteries but point its **traces at Shepherd's OTLP endpoint** via Faro's own OTLP exporter.
Only the RUM payload reaches a Faro listener, so the sprawl-prone path carries a fraction of the
volume while traces ride the scalable one.

**If and when Faro is built, three options — sharding recommended:**

1. *Port per tenant.* Trustworthy: identity is which socket received the bytes, and the gateway
   controls that via `backendRef`. Sprawls.
2. *Shared port, payload-derived tenant* (`extra_log_labels` → `stage.tenant`). No sprawl, but
   tenancy is **client-asserted**: a browser on tenant A's URL can claim another tenant's `app`
   and land in their Loki tenant. A cross-tenant write, not merely noise. Permitted only if
   documented explicitly as *not* an isolation boundary.
3. **Shard tenants across instances (recommended).** Keeps option 1's structural isolation,
   bounds the sprawl, and buys independent blast radius and restart domains. The ceiling is the
   Kubernetes Service object size rather than a documented port count, so the shard size must be
   **measured before it is promised**.

Grafana publishes no tenant-isolation guidance for Faro; the only related knob is
`rate_limiting { strategy = "per_app" }`, which exists for fairness between apps sharing a
gateway and treats `app` as an identifier, not a boundary. Grafana Cloud's own Frontend
Observability gives each stack its own endpoint — endpoint-per-tenant, the same conclusion
option 3 reaches.

**Per-entry dynamic tenancy on the logs path is expressible**, and this corrects an earlier
assumption that `loki.write.tenant_id` was the only lever: `loki.process` has `stage.tenant`
with `value` (static), `label` (from a log label) and `source` (from an extracted field), so one
`loki.write` can serve many tenants. The constraint was never the exporter — it is finding a
*trustworthy* label to feed it.

### D11 — Tenant identity belongs to the org, and only an app admin sets it

`tenant_id` — the value injected as `X-Scope-OrgID` — is a column on `orgs`
(`0013_org_tenant_id`), set by an **application administrator** at org creation or once
afterwards via `SetOrgTenantID`. `CreateTenantRoute` reads it from the org; the request field is
**reserved in the proto**, so a caller cannot supply one at all.

This replaces free-form caller-supplied tenancy, which a final review caught while still latent.
Under the old shape an org admin — or an apply-capability service account in their org — could
mint an active route whose injected header named a **different org's** tenant. Nothing checked it,
because there was no org→tenant mapping to check against. The moment `ApplyRoute` gets a
production caller, such a row becomes a gateway-blessed endpoint stamping the victim's tenant onto
attacker-chosen data: a cross-tenant write authorized by the wrong org.

Three properties make it hold, and each is enforced where it cannot be skipped:

- **Unrepresentable, not validated.** The proto field is reserved, so the value has no way onto the
  wire. Validating a field you still accept is the weaker fix.
- **One tenant, one org.** A partial unique index on `orgs.tenant_id`; two orgs sharing an identity
  would merge their telemetry just as surely as a forged header.
- **Set-once.** `SetOrgTenantID`'s `WHERE tenant_id IS NULL` is what enforces it, so a concurrent
  second caller loses cleanly. Changing an org's tenant after routes exist would leave every
  applied `HTTPRoute` injecting the old value — still routing, now wrong, which is harder to notice
  than an outage.

The charset is Grafana Mimir's own documented rule (alphanumerics plus `! - _ . * ' ( )`, ≤150
bytes, `.`/`..`/`__mimir_cluster` reserved), verified against their docs rather than invented:
`internal/gateway.ValidateTenantID` is the single definition, and 0013's CHECK mirrors it.

An org with no tenant identity cannot have routes at all — `CreateTenantRoute` refuses and names
the app-admin action. That is deliberately a loud stop rather than a backfilled default: inventing
a tenant for an existing org would silently decide where its telemetry lands.

## 3. The model

```
client (browser / lambda / service)
   │  https://gw.example/{kind}/{tenant-route}/…
   ▼
Gateway (Gateway API, customer- or platform-owned)
   │  HTTPRoute rendered by Shepherd:
   │    · URLRewrite   strips the /{kind}/{tenant-route} prefix
   │    · HeaderModifier injects X-Scope-OrgID: <tenant>
   ▼
receiver-tier Alloy (role=receiver, pipeline served by Shepherd)
   │  otelcol.receiver.otlp / faro.receiver → batch → destinations
   ▼
destination (Mimir / Loki / Tempo / Pyroscope)
```

The route prefix is an **identifier, not an authorizer**. A token in a URL leaks via Referer
headers, proxy logs and browser history; RUM endpoints are semi-public by nature. The real
controls at the edge are origin allowlists, rate limits, and rotation — say so in the docs
and enforce it in the render, do not imply the prefix is a secret.

## 3a. Who this serves

The gaps in the last column are what W9–W11 exist to close.

| Actor | Wants | Today | Gap |
|---|---|---|---|
| Platform / observability engineer | Owns Shepherd, destinations, tenants, guardrails | `app-admin` — fits | Fleet health (W5) |
| Tenant / team admin | Owns one org's pipelines, destinations, quota | `org-admin` — fits | — |
| **Service owner (dev team)** | "Get my app's telemetry flowing"; does not know Alloy | `org-admin` (edits *everything*) or `org-reader` (nothing) | **Scoped write** (W10) |
| Cluster operator | Owns the clusters collectors run in | Partial | Chart values generator (W9) |
| SRE / on-call | "Did collection silently break?" | Served config only | Runtime health (W5) |
| Security / compliance | Credential handling, egress, who changed what | Audit + RBAC + containment — strong | Read-only auditor role (W10) |
| **FinOps / cost owner** | Telemetry volume is a top-3 cloud cost | Nothing | Unserved — see below |
| Collector (machine) | Identity, auth, fresh config | Solid (`agentapi` basic auth) | — |
| CI / automation (machine) | Stable API, idempotent ops | Connect API + GitOps | Service accounts (W10) |
| **AI agent (machine)** | Read state, propose changes safely | Must borrow a human session | W10 + W11 |

**Why agent actors need little new safety machinery.** Agent safety requirements are the same
as careful-human requirements, and this repo already over-invested in those: try-before-commit
is unusually complete (render → validate against the *real pinned Alloy binary* → simulate in a
contained sandbox → inspect captured series, all before a collector sees anything); blast radius
is computable in advance because matchers are declarative; and every change is attributed and
reversible. What is missing is **identity and scoping**, which is W10.

**FinOps, deliberately not yet a workstream.** Shepherd authors what gets collected, so it sits
at the one point where cost is decidable *before* it is incurred — and the sandbox already runs
a pipeline and captures series, so "this pipeline will add ~N series" at review time is within
reach and nobody else in the chain can offer it. It earns a workstream when a user asks for it,
not before.

## 4. Workstreams

Dependencies are strict: a workstream may not start until its prerequisites have passed their
gates (§6, §7).

| # | Workstream | Depends on | Summary |
|---|---|---|---|
| **W1** | Signal derivation + role enforcement | — | Derive each pipeline's signal set from the schema's wire types (`prom.metrics`, `loki.logs`, `otel.*`, `pyroscope.profiles`); merge engine refuses a metrics pipeline aimed at a `role=logs` collector. Turns `role` from a label into a contract. |
| **W2** | Destination templates + tenant bindings | — | One platform-owned endpoint+auth secret; N tenant bindings overriding only `tenant_id`. The `destinations` table already carries `url`/`tenant_id`/`secret_name`/`auth_mode`; this adds inheritance, so teams get a destination without seeing the credential. |
| **W3** | Gateway API foundation | — | Version+channel pin, CRD detection (D3), `HTTPRoute` renderer, and the kind conformance harness (G3/G4). No product surface yet — this is the substrate. |
| **W4** | Receiver tier + tenant routes | W1, W3 | Receiver-tier Alloy (pipelines with `role=receiver`). **OTLP first** (D10): one listener serves N tenants with gateway-injected, unspoofable tenancy. Faro is deferred until a user asks, and then sharded (D10). Supports **both** gateway ownership modes (D8) and **both** prefix formats (D9); operator-owned mode must verify route attachment rather than assume it. First infrastructure Shepherd manages directly, in our own cluster. |
| **W5** | Beacon + outcome verification | — (W1 makes it more useful) | Ingest endpoint in `agentapi` (D6), baseline pipeline, inventory storage + expiry, and the fleet-health surface it unlocks. **Plus the other half of the same question**: an optional Grafana service-account token so Shepherd can query the destination and confirm data actually arrived. The beacon proves the collector runs what we think; the query proves the data landed — neither is sufficient alone, and building them separately produces two partial answers. |
| **W6** | Three-way reconciliation | W1, W5 | Reconcile **declared** (attributes) vs **served** (our pipelines' signals) vs **observed** (beacon inventory). Contradictions surface as findings — this is what catches a BYO logs collector actually running `prometheus.scrape`. |
| **W7** | Onboarding artifacts | W4 | "Connect an app": render endpoint+headers+tenant into Lambda env, Terraform/SAM/CDK, container/k8s env, SDK inits — OTLP-shaped per D10. A Faro snippet ships only alongside Faro support, and should document the hybrid (Faro RUM + traces over OTLP). Golden-tested; every emitted endpoint must resolve to a really-rendered route. |
| **W8** | Wizard catalog fan-out | W1 | The cluster-metrics / pod-logs / database / blackbox / self-monitoring wizards. W1 first so a wizard cannot generate a pipeline that lands on the wrong collector role. |
| **W9** | k8s-monitoring chart values generator | — | Guided setup emitting **Helm values** for Grafana's k8s-monitoring chart, pre-wired to this Shepherd (remotecfg endpoint, token, cluster/role/tenant attributes). Same guided-form UX as a wizard, different commit target: a wizard's `Commit()` returns pipeline contents Shepherd *serves*; this returns a deployment artifact that runs in the customer's cluster. Serves the BYO scenario, so it may reasonably run ahead of W4. |
| **W10** | Teams, scoped identity, machine actors | — | Teams keyed by IdP group (extending `group_assignments`) that can *own* pipelines; scoped write for the service-owner persona; service accounts + capability scoping (propose vs apply) for machine callers; two-part attribution. Blocks W11. |
| **W11** | Agent interface (MCP, read + propose) | W10 | An MCP server over the existing Connect API in read-plus-propose mode — list collectors and health, render/validate, simulate, compute blast radius, propose a revision. **No apply.** Thin adapter; the work is interface and identity, not new safety machinery. |

## 5. What each workstream must not do

- **W1** must not silently downgrade a mismatch to a warning "for compatibility". If the
  merge engine cannot prove the signal set, it refuses and says why.
- **W3** must not render routes that a controller may ignore (D3), and must not depend on any
  Experimental-channel feature (D2).
- **W4** must not enable the receiver tier by default until R3 signs off.
- **W5** must not persist raw samples, ever. Parse, project, discard.
- **W5** must not make Grafana a hard dependency, and must not grow into dashboard or alert-rule
  management — that has no natural boundary and Grafana answers it better. Grafana absent means
  no outcome verification, never reduced function. Document and default to the minimum token
  scope (query-only).
- **W7** must not emit an endpoint that no route serves, and must not hardcode ADOT ARNs (D5).
- **W9** must not attempt full coverage of the chart's values surface — that is maintaining a
  mirror of someone else's chart, unbounded and permanently rotting. It emits a **layering file**
  meant to sit alongside the operator's own `-f` values, covering exactly cluster observability
  and application observability. New features enter on demand, never speculatively.
- **W10** must not ship a capability scope enforced at a single chokepoint. Propose-vs-apply is
  proven per write path or it is not proven (see G12).
- **W11** must not gain an apply path "temporarily".

## 6. Conformance gates (machine-checked)

Every gate is a test that **fails when the control is removed** — the repo standard. A gate
without a demonstrated red run is not a gate.

| Gate | Proves | Where |
|---|---|---|
| **G1** | `GATEWAY_API_VERSION` agrees across `versions.env`, chart, docs and tests | Makefile guard, `make lint` |
| **G2** | Shepherd refuses a cluster whose Gateway API CRDs are absent/older/wrong-channel, with an actionable message | Go unit + kind suite |
| **G3** | A rendered route **actually routes**: POST through the gateway arrives at the backend with the prefix rewritten and `X-Scope-OrgID` injected | kind suite, real controller |
| **G4** | **Tenant isolation, kill-probe style**: a request to tenant A's prefix never arrives tagged as tenant B; deleting the header filter turns G3/G4 red | kind suite |
| **G5** | Beacon ingest rejects unauthenticated writes, enforces the rate/size caps, and stores no raw samples | Go integration |
| **G6** | A pipeline whose signals contradict the target role is refused; removing the check makes named specs red | Go unit + merge integration |
| **G7** | Every onboarding artifact renders to goldens, and every endpoint it emits resolves to a rendered route | Go golden tests |
| **G8** | `K8S_MONITORING_CHART_VERSION` agrees across `versions.env`, the vendored `values.schema.json`, docs and tests; `chart-verify` fails on upstream drift | Makefile guard + scheduled job |
| **G9** | Generated chart values validate against the vendored `values.schema.json` and `helm template` cleanly, per feature combination | Go golden tests |
| **G10** | **End-to-end BYO**: installing the chart with generated values in the kind suite produces an Alloy that registers with Shepherd and receives a pipeline | kind suite |
| **G11** | A team member can write what their team owns and CANNOT write another team's resources in the same org; removing the ownership check makes named specs red | Go integration |
| **G12** | A propose-scoped token cannot apply — asserted **per write path**, not at one middleware chokepoint | Go integration |
| **G13** | Audit records both halves of a delegated action; a machine action with no on-behalf-of is rejected or recorded as such, never silently attributed to a human | Go integration |
| **G14** | The MCP server cannot apply: every mutating procedure refused with a propose-scoped token, asserted per procedure | Go integration |
| **G15** | A proposal round-trips: proposed → visible as a revision/PR with its simulation result → a human applies it → audit shows both actors | Go integration |

The kind suite already installs a CNI and proves NetworkPolicy enforcement before trusting a
denial (`e2e/k8s/`, `docs/kind-test-environment-plan.md` §4, §8b). G3/G4 extend the same
harness: install the pinned Gateway API CRDs plus a controller (Envoy Gateway is the
default), then assert the *observable effect* of a route — never that the YAML rendered.

## 7. Review gates (human sign-off between sessions)

A workstream is not done when its tests pass; it is done when its gate is signed off.

| Gate | When | What must be reviewed |
|---|---|---|
| **R1** | After W3, before W4 | Route/security model: URL-as-identifier caveat documented and not overstated, rate limits, origin allowlist, what a leaked prefix does and does not grant. **Now also**: which prefix format is the default (D9), and — for operator-owned gateways (D8) — that a route which fails to attach is surfaced as a refusal rather than silent non-routing |
| **R2** | After W5 ingest lands | Data review of the beacon: exactly what is stored, retention/expiry, proof no config text or secret is retained, ingest abuse surface |
| **R3** | Before the receiver tier defaults on | Same bar F5 had to clear: containment, blast radius, and a documented off-switch that is itself tested |
| **R4** | Every session, at the end | No doc claims a control that is not wired. This repo has shipped that failure once (claimed CI gates that ran nowhere); the ledger entry and the code must agree |
| **R5** | Before W10's scoped write ships | Permission model review: does the team/ownership model actually deny what it claims, and is every write path covered rather than one chokepoint (G11, G12)? |
| **R6** | Before the MCP server is reachable by any non-human | Agent-actor review: capability scope, rate/budget limits, and that no apply path exists — plus how a proposal is attributed when the human on whose behalf it acts is absent |

### Decisions taken against these gates so far (2026-08-22)

Recorded here so a signer sees what has already been settled and what is still theirs.

- **R1 / D11 — tenant identity is org-scoped and app-admin-set.** The route/security model's one
  remaining hole (any org admin could mint a route injecting another org's tenant) is closed by
  D11. What still needs a human at R1 is the prefix-format default (D9) and the URL-as-identifier
  framing.
- **R3 — still open, and its checklist grew.** Beyond containment and a tested off-switch, R3 must
  now cover: a NetworkPolicy (or equivalent) making the gateway the only ingress to a pass-through
  receiver, and an end-to-end proof that pass-through tenancy works against a **real Alloy**, not
  an echo server — the batch-processor defect passed every static gate this plan had.
- **R5 — resolved: a machine may not issue credentials.** An apply service account cannot call
  `CreateServiceAccount`. Issuing a credential grants identity, and every machine write is checked
  against that credential's `created_by` precisely because a human put their name there. A
  machine-minted credential would carry `svcacct:<parent>`, so the delegation chain would stop
  bottoming out at anyone accountable while each individual link still validated. Revoking the
  parent would not revoke the child either.
- **R6 — partly resolved.** An agent's proposal is now audited: `ValidatePipeline` writes a
  `pipeline.propose` row **when the caller is a machine identity**, carrying both halves of the
  delegated action. Human callers write nothing, because auditing every keystroke of UI validation
  produces a trail nobody reads. Still open for a human: whether agent proposals should ever reach
  the auto-applying `gitsync` path (recommendation: no) and what budget/rate limits an agent actor
  should carry.

### The beacon reaches real deployments at v0.0.2 — and R2 is unsigned

A release review caught a contradiction this plan was making about itself. §9 and
`docs/project-status.md` both said "nothing here is user-reachable until the gates are signed", but
the beacon is **on by default in the shipped binary**: `/beacon/v1/write` is mounted
unconditionally and every claimed collector is served the baseline pipeline. Tagging v0.0.2 makes
that true for every upgrader, while **R2 — the review of exactly what the beacon stores — has not
been signed.**

That is D6 working as designed ("the collector we know nothing about is precisely the one that
would never opt in"), not a bug. But "not opt-in" and "no way to decline" are different claims, and
only the first was decided here. So v0.0.2 ships `server.beacon_disabled` (default `false`, exposed
as a chart value): the beacon stays on by default per D6, and an operator who cannot yet answer for
an ingest endpoint and an inventory table can turn it off without downgrading. Disabling mounts no
endpoint at all rather than one that always refuses — an endpoint that exists still advertises the
surface and still has to be reasoned about. Red-run proven.

**This does not close R2.** The data review is still owed, and the off-switch is a courtesy to
operators, not a substitute for it.

### `shepherd-mcp` is deliberately not in the release archives

The release builds `shepherd` and `shepherd-simulator` for linux/amd64 and linux/arm64 only.
`cmd/shepherd-mcp` is absent on purpose: it is a client-side stdio process that runs under an
editor's MCP integration, so linux-only server archives would be the wrong vehicle even if W11 were
signed off — and R6 is open. Build it from source until then. Recorded here because an omission
nobody wrote down reads as an oversight the next time someone checks.

## 8. How sub-agents work on this

Conventions that made the previous multi-agent sessions land cleanly. They are not optional.

1. **Disjoint territory.** Each parallel agent owns a declared, non-overlapping file set and
   modifies nothing outside it. Cross-cutting edits (renames, path sweeps) are done by the
   orchestrator after the parallel phase, never concurrently.
2. **Every control ships with its red run.** State the exact one-line revert that disables the
   control, run it, and report the observed failure verbatim. "I believe this would fail" is
   not a red run.
3. **A detected gap is a finding, not a test to weaken.** If a probe fails, report it and
   diagnose. Never relax an assertion to reach green. Two real defects (a Felix convergence
   race, `kubectl debug --attach` swallowing exit codes) were found exactly this way.
4. **Structured report every time**: `done[]`, `skipped[]` with reasons, `decisions[]`,
   `verification` with the exact commands and their outcomes.
5. **Serialize the cluster.** `make e2e-k8s` and image builds must not run concurrently with
   another agent's kind session. The orchestrator sequences them.
6. **Verify version-dependent facts against the pinned artifact, not from memory.** Gateway
   API conformance levels and channels move between releases (D2's note). The same discipline
   that caught a darwin-flavoured schema artifact applies here.
7. **Update this file's ledger (§9) and, when a session teaches something, append to §10.**
   Product-level status goes in `docs/project-status.md`; do not start a second ledger.
8. **Docs land in the same commit as the control they describe.** Not the next session.
9. **Actions minutes are a budgeted resource (3000/month).** GitHub bills every job
   separately, so a workflow's parallel jobs multiply. Before adding a job or a schedule,
   state its per-run billed cost and what it buys; prefer a paths filter or an existing job
   over a new one. Never buy coverage with a nightly schedule that a paths filter already
   provides — and never trade *coverage* for minutes: reduce redundant executions, not gates.
   `make lint`, the full Go suite and the kind suite all run locally for free; CI is the
   backstop, not the first place a change is tested.

## 9. Ledger

Status values: `proposed` → `in progress` → `gated` (built, awaiting its review gate) →
`done`. Update in the same commit as the work.

| Step | Status | Gates | Notes |
|---|---|---|---|
| W1 signal derivation + role enforcement | **done** | G6 ✅ | `internal/signals` (derivation + role policy table) and `internal/merge`'s `WithRoleEnforcement`, wired on **both** serve paths — `internal/mgmtapi`'s write-time recompute and `internal/agentapi.Service.recomputeServeCache`, the lazy path `GetConfig` takes when `serve_cache` is dirty. **G6 closed** by an integration test that drives the real dirty-window poll rather than calling `merge.Assemble` directly (`internal/agentapi/service_test.go`), red-run proven: disabling agent-path enforcement fails it with "a metrics pipeline reached a logs-role collector". Three defects found and fixed by review along the way — `Derive` skipping `foreach`/`declare` bodies (empty-but-*proven* signal set, enforcement passing trivially), `WithRoleEnforcement(nil)` silently disabling the guard it was asked to enable, and the agent path being unenforced entirely. |
| W2 destination templates + tenant bindings | **done** | — | Merged to main as PR #11 (`0008_destination_bindings`). The security property is structural, not conventional: `destination_bindings` is a child table with **no** `url`, `secret_name`, `secret_namespace`, `auth_mode` or extra columns — a binding cannot override the credential because there is nowhere to write one. `destinations` is unmodified, so existing rows, the dev seed and e2e fixtures keep working untouched. The API layer *refuses* a binding that tries to set a credential-bearing field rather than ignoring it (red-run proven: removing the check lets an attacker-supplied `secretName` create a binding with 200 instead of 400). Template FK is RESTRICT, not CASCADE, so deleting a shared template with live bindings fails loudly instead of quietly breaking N teams. **This row said `proposed` until 2026-08-22 while the code was already on main** — the drift R4 exists to catch, found while picking the next workstream. |
| W3 Gateway API foundation | **done** | G1 ✅, G2 ✅, G3 ✅, G4 ✅ | Pin + conformance facts verified against the released CRDs (2026-08-21): at the v1.4 floor the Standard channel carries `URLRewrite`/`ReplacePrefixMatch` but **no CORS filter** (it reaches Standard only in v1.6.1), confirming D2. `make check-gateway-pin` (G1) and `internal/gateway.CheckSupport` (G2/D3) both red-run proven. `RenderHTTPRoute` emits a typed Gateway API v1 `HTTPRoute` that rewrites the tenant prefix and **sets** (never appends) `X-Scope-OrgID`. G3/G4 proven in the kind suite against a real controller: the backend observed `path="/v1/traces" X-Scope-OrgID="acme"` while the client sent `"evil-client-value"`, tenant B's prefix arrived tagged `globex` despite the client claiming `acme`, and the kill probe confirms removing the filter lets the client value pass straight through. |
| W4 receiver tier + tenant routes | **gated** (awaiting R1) | G3, G4 · R1, R3 | Segment generation + storage + mgmtapi surface built this session: `internal/gateway/segment.go` (D9's two formats, crypto/rand, `ValidateSegment` enforced at generation AND independently by 0009_tenant_routes' segment CHECK constraint), the `tenant_routes` table (org/tenant/kind/segment/status/timestamps, rotation modeled via `deprecated`+`valid_until`+`rotated_from_id`, D8's `gateway_mode` column expressing both ownership modes without a later migration), and `TenantRouteService` (create/list/rotate/revoke, org-reader/org-admin split). **Not built**: the receiver-tier Alloy pipelines, the actual Gateway/HTTPRoute apply to Kubernetes (RenderHTTPRoute from W3 is unused by this slice — segments are opaque values to it already), and the janitor sweep that flips an expired `deprecated` row to `revoked` (RevokeTenantRoute does it on demand instead). G3/G4 stay W3's (already closed); this slice adds no new kind-conformance surface. **2026-08-22 — the apply + pass-through slice landed**, closing the two pieces that were named as missing above. (1) `internal/gateway/apply.go`: `ApplyRoute` checks the cluster's actual CRD annotations (D3) *before* applying, renders, applies, then polls `status.parents[]` until the route is verified ATTACHED — `Accepted=True` **and** `ResolvedRefs` not-False, since a route can be Accepted while its backendRef resolves nowhere. Missing status is pending (polled through), a definitive refusal short-circuits, and **deadline expiry returns an error, never an assumed success** (red-run proven: replacing the deadline branch with `return nil, nil` fails with `want a deadline-expiry error, got nil`). Both D8 ownership modes take the same code path with no branch — who created the Gateway is invisible from there. Proven in the kind suite against a real NGF controller in `e2e/k8s/route_apply_test.go`: a route at a Gateway whose `allowedRoutes` does not admit our namespace came back `Accepted=False (NotAllowedByListeners)` as a loud refusal *and* the test confirmed the HTTPRoute object itself exists, so this is a detected refusal rather than a failed create; the admitting case verified attachment and then actually delivered traffic (`path="/v1/traces" X-Scope-OrgID="acme"`). (2) D10 pass-through tenancy in `internal/receiver`: `OTLPPipeline.Mode` selects `TenancyStatic` (unchanged zero value, byte-identical goldens) or `TenancyPassThrough`, which wires `include_metadata` on every listener and one `otelcol.auth.headers` `from_context` component onto **every** exporter — derived from `Mode`, not a per-exporter opt-in, so a half-wired pass-through pipeline is not a representable state. `Validate` refuses pass-through combined with a hardcoded literal tenant header (red-run proven). **Tenant identity is no longer caller-supplied (D11, 2026-08-22)**: `CreateTenantRoute` reads it from `orgs.tenant_id`, set once by an app admin, and the request field is **reserved in the proto** so it cannot be sent at all. This closes the cross-org hole a final review found while it was still latent — an org admin could previously mint a route injecting another org's tenant. Red-run proven (putting the field back lets a caller stamp `globex` on an `acme` org's route), with a migration-level suite pinning the charset (Mimir's own documented rule), the one-tenant-one-org unique index, and that untenanted orgs' NULLs do not collide. An org without a tenant identity is refused a route with a message naming the app-admin action, rather than getting one with an empty header. **Still not built**: the janitor sweep, and Faro sharding (deferred by D10 as demand-driven). **R1 is ready for sign-off** — its operator-owned-attachment obligation is now built and cluster-proven; R3 remains open until the receiver tier defaults on. |
| W5 beacon ingest + inventory | **gated** (awaiting R2) | G5 ✅ · R2 | Built this session: the ingest endpoint (`internal/agentapi/beacon_handler.go`, `POST /beacon/v1/write`, mounted at router root next to the Connect API — same trust boundary and Basic Auth as `collector.v1`, resolving open decision §11.3), the structural no-raw-sample projection (`internal/beacon.Project` returns only `ComponentObservation{ComponentName, Healthy}` — pinned by a reflection test that fails the moment a numeric field is added), both caps (body-size, doubled via `http.MaxBytesReader` **and** an independent bound inside `beacon.DecodeWriteRequest`; per-instance rate limit via `golang.org/x/time/rate` keyed on the authenticated agent-token id), storage + expiry (`0010_beacon_inventory`, keyed by `(token_id, instance_label)` — not `collector_instances.id`, which an isolated remotely-served pipeline cannot read, per docs/spec.md's self-contained-pipeline note — swept by `internal/agentapi`'s existing `Sweeper`), and the baseline pipeline (`internal/beacon.RenderBaselinePipeline`: `prometheus.exporter.self` → `prometheus.scrape` → `prometheus.relabel` (keep only `alloy_component_controller_running_components`, verified against `grafana/alloy` tag `v1.18.1` source, not memory) → `prometheus.remote_write` with `basic_auth` read via `sys.env("SHEPHERD_AGENT_TOKEN_ID"/"_SECRET")` — no plaintext credential is ever renderable since Shepherd only ever stores `sha256(secret)`). **G5 closed** by a Go integration suite against a real database (`internal/agentapi/beacon_handler_test.go`), every control red-run proven: bypassing `verifyBasicAuth` turns the unauthenticated-write spec red; removing the size cap **inside `beacon.DecodeWriteRequest`** turns `TestDecodeWriteRequest_BodyTooLarge` red — **corrected 2026-08-22**: this row previously claimed removing *either* enforcement point turns a spec red, and a final review showed that does not reproduce. Deleting the `http.MaxBytesReader` wrap leaves the whole agentapi suite green, because the inner cap reads through an `io.LimitReader` and still answers 413. Memory stays bounded either way, so the wrap is genuine defense in depth — but it is **not independently covered**, and a claim of a red run that does not reproduce is exactly what §8 rule 2 exists to prevent. Recorded honestly rather than restated; `RateLimiter.Allow` hardcoded to `true` turns the rate-limit spec red (at both the `internal/beacon` unit level and the integration level); a `Value float64` field added to `ComponentObservation` turns the structural test red; deleting the CHECK/FK/UNIQUE constraints from `0010_beacon_inventory.up.sql` each turn their respective migration spec red; removing the sweeper's expiry call turns the sweep spec red. **The W1 seam reopened and was closed the same way**: `beacon.AppendBaseline` is the one function both `internal/agentapi.Service.recomputeServeCache` (lazy) and `internal/mgmtapi.PipelineService.recomputeOrgCaches` (eager) call, so the baseline reaches both serve paths from one implementation rather than two. **Scoped out deliberately**: `stage3Check` (mgmtapi's pre-flight dry-run validator) does not get the baseline appended — it validates candidate *user* pipeline content against the real alloy binary and never writes `serve_cache`, so it is not a served-config path in G5's sense. The baseline is also not served to an *unclaimed* cluster (before an org claims it via `GetConfig`'s early-return empty-config branch) — only claimed collectors reach `recomputeServeCache`. **Not built**: the fleet-health surface W5's summary names (`ListBeaconInventoryByToken` exists as a query but has no mgmtapi/UI surface — that consumption is W6's three-way reconciliation, which depends on this table existing, not this slice's job) and D7's Grafana outcome-verification half (a separate, larger slice).  **D7 (the other half of W5) landed 2026-08-22**: `internal/grafana` verifies the outcome — did the data actually arrive — by querying the destination through Grafana, the same "assert the observable consequence" standard as `alloy validate` is not `alloy run`, extended past the collector into production. The design point that matters is **three outcomes, not two**: arrived / did not arrive / could not determine. Any error — unreachable, timeout, non-2xx, decode failure, a datasource-level error inside a 200, no token configured — maps to *could not determine*, and `OutcomeUnknown` is the zero value so a half-built result cannot default to a false negative. Collapsing that third case would report a broken Grafana as a broken pipeline. Every call is bounded by `Client.do`'s own timeout regardless of the caller's deadline (red-run proven against a slow server with no caller deadline), the token is encrypted at rest following `git_credentials`' precedent and redacted in `String`/`GoString`, and the result types structurally cannot carry it. With no Grafana configured everything else still works and verification reports *unknown* — §5's "Grafana absent means no outcome verification, never reduced function", red-run proven. Migration 0011. **Not wired**: no RPC/UI surface yet. |
| W6 three-way reconciliation | **gated** (awaiting wiring) | G6 | `internal/reconcile` is a pure library — no database, HTTP or clock — that compares **declared** (the collector's role), **served** (its pipelines' signals, via `signals.Derive`/`signals.Enforce`, reused rather than re-derived) and **observed** (beacon inventory). Every finding names which two sources disagree and what each claimed. **The load-bearing property is that absence is not disagreement**: `servedVsObserved` only ever ranges over what was observed, so a collector that never reported — or whose inventory fully aged out — produces zero findings by construction, not by a check that could be edited away. Red-run proven, and the reviewer independently reintroduced the naive "served minus observed = removed" bug and confirmed it goes red immediately. A stale-but-present observation still counts as positive evidence, flagged rather than dropped; an unproven served signal set is widened to worst-case, mirroring `internal/merge/enforce.go`; an unrecognized role fails loud rather than becoming a routine finding. **Not wired**: no caller supplies the three inputs yet. |
| W7 onboarding artifacts | **gated** (awaiting wiring) | G7 ✅ | `internal/onboarding` renders seven artifacts from one spec — env vars, Lambda console, Terraform, SAM, CDK, Kubernetes, and SDK notes — all OTLP-shaped per D10. **G7's load-bearing half is the second one**: every emitted endpoint is derived from `internal/gateway.RouteSpec.PathPrefix()`, the same function `RenderHTTPRoute` uses to build the real path match, so an onboarding doc cannot drift into handing out URLs that 404. Red-run proven: restating the path as a literal fails with `endpoint path "/otlp-acme-a1b2c3d4" does not match the route's actual rendered path "/otlp/acme-a1b2c3d4"`. A second property test scans every `https://` URL across all seven rendered artifacts and requires each to resolve to the route's path or a `/v1/<signal>` child of it. The SDK-suffix boundary is handled explicitly: artifacts hand SDKs the **base** endpoint (they append `/v1/traces` themselves), and only the SDK notes use the full per-signal form, for the per-signal override variables. No ADOT ARNs, regions or account IDs are hardcoded (D5) — each Lambda artifact emits a caller-filled parameter plus a link to AWS's published table; red-run proven by `TestNoADOTArnHardcoded`. No Faro snippet is emitted: `Render` refuses any non-OTLP route kind, since Faro support does not exist yet. **Not wired**: nothing imports the package yet — no HTTP/RPC/UI surface, which is why this is `gated` rather than `done`. |
| W8 wizard catalog fan-out | **gated** (awaiting UI) | G6 ✅ | Five catalog wizards — cluster-metrics, pod-logs, database, blackbox, self-monitoring — plus the existing app-observability. **G6 is enforced structurally rather than per wizard**: `Register` is the only path onto the default registry and always wraps in `roleEnforced`, whose `Commit` derives the generated content's real signals and checks them before any caller sees a result, so a future wizard that forgets to self-check is still covered. Each wizard's declared component/attribute paths are checked against the pinned artifact AND against a committed golden, and goldens are validated by the real pinned Alloy binary (Fail, never Skip). Two defects found by review and fixed here: (1) the app-observability role dropdown offered `logs`, which its unconditional `prometheus.scrape` block can never satisfy — a dead end dressed as an option; removed, and the spec `"every offered role is satisfiable"` now requires every offered role to be committable by some valid state, which generalizes past the one bad value. (2) Wrapping every wizard in `roleEnforced` turned `RenderWizard`'s preview of syntactically invalid output from 200-with-diagnostics into a 400, because `signals.Derive` returns an error for unparseable text. `signals.ErrParse` now makes that case distinguishable and role enforcement passes it through to Stage 1, which reports it with line-level diagnostics; a second test pins that the exemption did not widen into ignoring real mismatches. |
| W9 chart values generator | **gated** (awaiting UI + G10) | G8 ✅, G9 ✅ | `internal/chartvalues` emits a **layering file** for Grafana's k8s-monitoring chart — `cluster.name` plus each selected role's `remoteConfig` block — deliberately not a mirror of the chart's values surface (§5). Chart 4.4.0 is pinned in `deploy/versions.env`; `make check-chartvalues-pin` and `make chart-verify` fail on drift, both red-run proven, and the reviewer independently confirmed the vendored `values.schema.json` is byte-identical to upstream's release and that every emitted key exists in it — cross-checked against the chart's own template and test fixtures rather than the schema alone. `TestHelmTemplateCleanly` does a real `helm pull` and `helm template` against generated output. **Outstanding**: G10 (installing the chart with generated values in the kind suite and watching an Alloy register) still needs a cluster run. |
| W10 teams + scoped identity | **gated** (awaiting UI) | G11 ✅, G12 ✅, G13 ✅ · R5 | Migration 0012: `teams` (org-scoped, keyed by IdP group — the same trust root `group_assignments` uses, extended up to org level rather than a parallel permission system), `pipelines.owner_team_id` (`ON DELETE SET NULL`, deliberately the opposite of 0008's RESTRICT: ownership is an administrative label, so deleting a team must neither delete the pipelines nor become impossible), `service_accounts` with a `capability` CHECK mirroring the Go constants, and `audit_log.on_behalf_of`. **G11**: `auth.AuthorizeOwnership` is called per handler; a team member can write what their team owns and gets 403 on another team's pipeline in the same org; unowned stays admin-only. **G12** — the gate whose own wording warns against a chokepoint — is asserted per write path: all 34 mutating procedures across 9 services call `requireWriteAuthorized` inline, and `TestEveryMutatingProcedureIsCapabilityClassified` derives the write set from the **live proto descriptors** rather than from the table it checks, so a newly added mutating RPC fails until someone classifies it. The independent reviewer enumerated the RPCs from the .proto files, confirmed the REST shim cannot carry a service-account identity at all, and red-ran two unsampled paths (`GitOpsService.CreateRepoLink`, `TenantRouteService.CreateTenantRoute`) to check the pattern generalizes past the three the suite exercises. **G13**: review found a real defect — `Shepherd-On-Behalf-Of` was copied verbatim into the audit row with no verification, so any apply-capability credential could attribute a write to any human it cared to name, including an org admin who never touched the system. **Rejecting an absent claim and verifying a present one are different guarantees, and only the first had been built.** The claim is now checked against `service_accounts.created_by` — the human recorded when an authenticated admin issued the credential, the one identity in the exchange the caller cannot influence — and two shipped tests that had asserted the vulnerable behaviour were corrected to model a real delegation. Red-run proven. **R5 resolved 2026-08-22**: a service account may not call `CreateServiceAccount` at all — issuing a credential grants identity, and machine writes are checked against `created_by` precisely because a human put their name there; a machine-minted credential would carry `svcacct:<parent>` and the chain would stop bottoming out at anyone accountable. **Known limits, deliberate**: one credential delegates for exactly one human (shared CI needs an explicit allow-list, additive); and an apply credential is an org-level power — team ownership does not further restrict machine writes, since resolving the delegating human's group membership without a session is new scope. The REST shim is stricter than the RPC path rather than looser, recorded in `router.go` rather than silently aligned. |
| W11 agent interface (MCP) | **gated** (awaiting R6) | G14 ✅, G15 ~ · R6 | `internal/mcp` exposes 12 tools over the existing Connect API plus `cmd/shepherd-mcp`. **G14's real enforcement is the CREDENTIAL, not the tool list** — and that distinction is the whole point. The MCP server authenticates as a propose-capability service account, so W10's `requireWriteAuthorized` refuses every mutating handler inline; `TestG14_ProposeCredentialRefusedOnEveryLiveMutatingProcedure` proves it by calling all 34 live-descriptor-enumerated mutating procedures **directly over the wire, bypassing the tool layer**, and getting 403 from each. A tool that doesn't exist yet, calling `CreatePipeline` directly, would still be refused. The static half (`TestNoToolReachesAMutatingProcedure`, deriving the mutating set from live proto descriptors) is defense in depth, not the safety. Red run re-measured on the merged tree: disabling the capability check flips **25 of 34**; the other 7 are AdminService, blocked earlier by the unconditional "no service account is ever app-admin" rule. **G15 is partly met and marked `~` deliberately**: the round-trip is proven end to end (propose → human applies through their own session → audit shows both actors, machine row with verified on-behalf-of and human row with none), and it reuses the existing `pipeline_revisions` path rather than inventing a second proposal object. But the audited propose step in that test is `create_sandbox_run`; `propose_pipeline_revision` — the tool actually named for this — composes `ValidatePipeline` + `PreviewMatches`, neither of which writes an audit row, so **an agent proposing a change to raw Alloy text leaves no trace that the proposal was made**. Disclosed in `propose.go` and left for R6 rather than papered over: adding audit to two read endpoints would also log every UI validation. **Finding folded in from review**: verifying the on-behalf-of claim inside `requireWriteAuthorized` covered apply-gated writes only. Propose-safe procedures never call it, yet `SimulateService.CreateRun` still stamps the claim into an audit row — so W10's fix left exactly one path able to record an unverified human. Verification now runs in the auth interceptor, where the claim enters, for every procedure; red-run proven against that quieter path. Also corrected: this test's own recorded red-run evidence said "20 of 27" when the tree gives 23 of 30 — evidence written from memory instead of re-run is the failure §8 rule 2 exists to prevent. **R6 partly resolved 2026-08-22**: an agent's proposal is now audited — `ValidatePipeline` writes a `pipeline.propose` row carrying both actors **when the caller is a machine identity**, while a human validating in the authoring UI writes nothing, because logging every keystroke produces a trail nobody reads. Red-run proven, including the human-writes-nothing half. **Not adopted, recorded as a finding**: the GitOps path is not a review-then-apply mechanism — `gitsync` applies `source="git"` pipelines automatically on every poll with no human gate, so routing agent proposals through it would silently auto-apply unreviewed agent changes. |

## 10. What building this taught us

### 2026-08-21 — what building W1 taught us

- **A fail-safe classification can be checked against the artifact instead of asserted.**
  `otel.any` (the generic OTel pipeline-data wire) is genuinely ambiguous — its runtime content
  depends on what is wired to it, which `internal/signals.Derive` deliberately does not evaluate.
  The naive fail-safe was "treat it as every signal, including Profiles". Instead of asserting
  that, the session queried the pinned schema artifact directly and found `pyroscope.profiles` is
  never produced or accepted by any component that also exposes an `otel.any` port — profiling in
  this artifact has no OTel-model bridge at all. So `otel.any` classifies as
  `{Metrics, Logs, Traces}`, not all four, and `TestOtelAnyNeverCarriesProfiles`
  (`internal/signals/wiretype_test.go`) pins the assumption to the artifact rather than to a
  model's recollection of it — exactly the discipline plan §8 rule 6 asks for, applied one level
  down from "which conformance table" to "which wire-type table".
- **Disjoint territory (plan §8 rule 1) cuts both ways, and W1 showed both edges.** A2's declared
  territory was `internal/merge/**` and `internal/mgmtapi/**`, so wiring `*schema.Registry` through
  `internal/agentapi.Service` — a third package — fell outside the session. The result was a control
  fully built, fully unit-tested and red-run-provable at the `merge.Assemble` level, while the one
  caller that actually serves config to real collectors (`recomputeServeCache`, reached from
  `GetConfig`'s recompute-when-dirty path) never asked it the question. **Two paths produce the same
  served config; enforcing one of them is not enforcement.** The territory rule did its job — nothing
  collided — but territory boundaries do not follow the blast radius of a control, and the orchestrator
  has to close that seam explicitly. It was closed in the same session, after review caught it.
- **A guard that reports "proven" over a set it never looked at is worse than no guard.** `Derive`
  skipped `foreach` and `declare` bodies, so a pipeline whose components all lived inside one
  produced an empty signal set that still answered `Proven() == true`. `Enforce` then passed it for
  every role. The tests were thorough about components they visited and silent about the ones they
  never reached — a reminder that "the tests pass" says nothing about the inputs the code declines to
  parse. Container recursion is now pinned by a red-run test.
- **Territory rules govern files, not branches or timing.** Two agents were given the same working
  tree; the second created its branch and switched the tree out from under the first, mid-run. Nothing
  was lost (disjoint territories made the work separable by path) but it cost a confusing debug cycle,
  and the orchestrator then compounded it by editing files while an agent was still live. §8 rule 1 is
  necessary and not sufficient: give each concurrent agent its own worktree, or serialize edits
  strictly. This is orchestrator discipline — no rule the agents follow can fix it.
- **Filename collation is not a dependency mechanism.** The CNI negative control gated dependent
  features through a package-level flag, relying on `go test` running files in filename order. The
  first feature added whose name sorted earlier (`gateway_test.go` < `negative_control_test.go`)
  failed instantly against an unverified CNI. Now `requireCNIVerified` runs the control on demand,
  `sync.Once`-guarded, which is order-independent and additionally makes `go test -run` of a single
  gated feature work — something the ordering scheme could never support.
- **Pick the controller that cannot widen the contract.** Envoy Gateway's Helm chart bundles
  Experimental-channel Gateway API CRDs by default; installing it would have silently put a wider API
  surface in the cluster than D2's Standard-channel contract allows, and the conformance suite would
  have proven routing against a surface real operators do not have. NGINX Gateway Fabric requires the
  CRDs to be installed separately, so what runs is exactly the `versions.env` pin.
- **A gate is closed by testing the path the user drives, not the path that is easy to test.**
  W1's unit tests exercised `merge.Assemble` directly and were thorough there, which is exactly why
  they could not see that the live serving path never called it with enforcement on. G6 only closed
  once a test polled `GetConfig` inside the dirty window — the same shape the compose and kind
  suites already use, and the same lesson `alloy validate is not alloy run` taught earlier: the
  assertion has to sit where reality does.
- **An opt-in control needs a loud failure mode for "asked for, not configured".**
  `WithRoleEnforcement(nil)` disabled the guard it was requested to enable. Not passing the option
  remains the way to opt out; passing it with nothing behind it is now an error.

### 2026-08-22 — what building W4's apply path taught us

- **Piping a gate through `tail` throws away its exit status.** `make e2e-k8s 2>&1 | tail -60`
  reports the exit code of `tail`, which is always 0. The suite had genuinely failed — `make: ***
  Error 1` was visible in the captured text — while the runner recorded success. A gate must be run
  so its own status survives: redirect to a file and check `$?`, or set `pipefail`. This is the same
  class as the CI jobs that once claimed to run and did not; a green signal that cannot go red is
  not a gate.
- **A misdirected probe wears the costume of the failure it is probing for.** The operator-owned
  attachment test addressed the gateway Service at `<svc>.<podNS>` while the Service lived in the
  operator's namespace, because the shared helper had one parameter where it needed two. Every
  request failed on DNS, and the test reported "attachment was verified but the route never
  actually delivered traffic" — which is precisely the product defect the assertion exists to
  catch, and precisely what it had not found. `curlThroughGatewayUntil` now takes `podNS` and
  `svcNS` separately and refuses to probe a Service that does not exist, so an addressing mistake
  fails immediately as an addressing mistake. The general shape: **when a negative result is the
  interesting result, the harness must rule out its own failure modes first**, or a broken test
  reads as a discovered bug.
- **"Verified attached" and "actually routes" had to be checked separately, and it paid off
  immediately.** The run above returned `Accepted=True` from a real controller while no traffic
  flowed. The cause turned out to be the harness, not the product — but only because the test
  asserted both halves did anyone find out. A route's own status is the controller's opinion; the
  backend's record of what arrived is the fact.
- **`golangci-lint run` ignores config keys it does not understand.** Chasing phantom findings, this
  session added a `run.exclude-dirs` key — valid in golangci-lint v1, moved in v2. `run` accepted it
  and silently did nothing; only `golangci-lint config verify` reported it. The same silence would
  hide a misspelled key meant to *enable* a check, which is a lint config that looks stricter than
  it is. `make lint` now runs `config verify` first (red-run proven: injecting an invalid key fails
  the target).
- **Delete the fix that turns out to do nothing.** The exclusion above was added to stop
  `.claude/worktrees/` being linted — then measurement showed Go tooling never walks dot-directories
  under `./...` at all, so the line was dead config. The phantom findings were golangci-lint replaying
  *cached* results from runs the agents had performed inside their own worktrees; `golangci-lint cache
  clean` was the actual fix. Shipping the inert config line would have left the repo claiming a
  control it does not have — §7's R4 failure in miniature, committed by the person auditing for it.
  **After removing agent worktrees, clear the lint cache before trusting `make lint`.**

### 2026-08-22 — what building W5 taught us

- **"Parse, project, discard" is provable by types, not just by review.** The obvious
  implementation of D6's ingest reads a `prompb.WriteRequest` and writes *something*; the
  question a reviewer actually has is whether that something could ever be a raw value.
  Making `Project` return `[]ComponentObservation{ComponentName string; Healthy bool}` — a
  type with no numeric field anywhere in its call graph — turns "we don't store raw samples"
  from an audit of every line that touches a `Sample.Value` into a single reflection test
  (`TestComponentObservationHasNoNumericField`) that fails the instant a future change adds
  one back. The same shape as W1's `Derive`/`Enforce` split: a guard is only as good as
  whether its failure mode is loud, and a type that cannot express the bad state is louder
  than any comment.
- **A cap enforced once is a cap one refactor away from unenforced.** `DecodeWriteRequest`'s
  size bound is checked twice on the production path — once by `http.MaxBytesReader` at the
  HTTP layer (so an oversized body is never fully buffered) and again independently inside
  the library function itself. The second check looked redundant while both were being
  written; the red run proved it was not merely defense in depth but the ONLY enforcement a
  caller gets if it ever forgets the `http.MaxBytesReader` wrap — exactly the "configured but
  not enforced" failure class the W5 brief named up front.
- **The served-config identity a table wants is not always the identity available at the
  point of writing.** `collector_instances.id` (the remotecfg wire id) looked like the
  obvious key for `beacon_inventory` — until docs/spec.md's "every remote pipeline must be
  self-contained" rule (remote pipelines run in an isolated component controller and cannot
  reference local components) turned out to mean a remotely-served pipeline cannot read the
  operator's *local* remotecfg `id` either. The next best identity was what a remote_write
  payload actually carries on its own: the authenticated agent-token id plus the `instance`
  label `prometheus.scrape` stamps on every series it scrapes. Worth restating the general
  form: a storage key chosen because it is the "natural" identity elsewhere in the schema has
  to be checked against what the specific write path can actually prove, not assumed.
- **The same W1 seam (§10, 2026-08-21) reopens for every "serve this to every collector"
  control, and has to be closed the same way each time.** D6's baseline pipeline needed to
  reach both `internal/agentapi`'s lazy recompute and `internal/mgmtapi`'s eager one, exactly
  like W1's role enforcement did. Rather than re-deriving the fix from scratch, this session
  factored the shared step into one function (`beacon.AppendBaseline`) both callers invoke,
  so "reaches both paths" is a property of there being one implementation, not of two people
  independently remembering the lesson. A third serve-shaped control in this codebase should
  default to the same move on day one rather than rediscovering the gap by review.

### 2026-08-22 — what the final review taught us

- **The most dangerous defect of the whole plan was invisible to every gate built for it.** D10's
  pass-through tenancy routes receiver → `otelcol.processor.batch` → exporter, and the batch
  processor does not forward client metadata unless told which keys to preserve. Without
  `metadata_keys`, `from_context` finds nothing at the exporter and *omits the header rather than
  erroring*: every tenant ships untagged — an outage against a strictly multi-tenant destination,
  a silent cross-tenant merge against one with a default tenant. It passed `alloy validate`
  (syntactically valid), passed the receiver tests (string assertions on rendered text), and
  passed the kind suite (whose backend is an echo server, not an Alloy pipeline). **"`alloy
  validate` is not `alloy run`" reappeared one layer up, in the control this plan was most
  confident about.** The lesson is not "add another test" but *which* test: a wiring invariant
  asserting that every stage between the listener and the exporter preserves the key the exporter
  reads. Fixed and red-run proven; the real end-to-end proof still needs a live Alloy in the kind
  suite, and R3 should require it.
- **A control that cannot be satisfied should be refused, not documented.** A pass-through pipeline
  with a gRPC listener is client-asserted tenancy in *every* deployment, because the only thing
  Shepherd renders that sets the tenant header is an `HTTPRoute`, and an `HTTPRoute` cannot front
  gRPC. That is not a gap an operator can close with configuration, so `Validate` now refuses it
  outright instead of the renderer emitting it with a caveat in a comment.
- **A comment that denies a control now wired misleads exactly as badly as one claiming a control
  that is not.** `internal/merge/enforce.go` still said the agent serve path was unenforced months
  after W1 closed that seam. R4 is usually read in one direction; it runs both ways.
- **Red runs are writes, and a reviewer reading the same tree will see them.** The orchestrator ran
  revert-and-observe cycles on `internal/mgmtapi/machine_auth.go` in the shared working tree while
  read-only reviewers were compiling tests against it. One reviewer consequently observed G14 fail
  twice — 23 of 30 mutating procedures accepting a propose credential, the exact signature of the
  capability check being disabled — and correctly refused to dismiss it. Resolving it cost a clean
  `git clone` of the reviewed commit and four repeat runs (all green) to prove the tree was sound.
  §10 already said "give each concurrent agent its own worktree"; the unstated half is that **the
  orchestrator is a concurrent agent too**, and a red run is the most destructive kind of write
  because it deliberately makes a control look broken. Run them in a scratch clone, or run them
  when nothing else is reading.
- **Sign off on a commit hash, not a branch.** Two of three final reviewers reported that the tree
  changed underneath them. Whatever the cause, a review of "the branch" is not a review of anything
  reproducible.
- **Recorded evidence decays, and it decays silently.** Two claimed red runs did not reproduce:
  "20 of 27" where the tree gives 23 of 30, and "removing either size-cap enforcement point turns
  the spec red" where removing the HTTP-layer wrap turns nothing red. Both were written from a
  true observation and drifted. Re-run the revert before repeating a number, and when a control is
  genuinely defense-in-depth with no independent coverage, say that instead of implying a red run.

### Rebase onto main — done 2026-08-22

This branch was cut before W2 merged. The rebase restored `0008_destination_bindings`, and the
enumeration test did exactly what it exists for: `TestEveryMutatingProcedureIsCapabilityClassified`
went red naming W2's three `DestinationService` binding RPCs, which had never been classified
because they did not exist on this branch when G12 was built. They are now `capabilityApply` and
call `requireWriteAuthorized` inline, like every other mutating handler.

Every count was **re-measured, not adjusted by arithmetic**: 34 mutating procedures across 9
services, and disabling the capability check flips 25 of them. The 9 that stay green are held by
*earlier* gates, and enumerating them is the point — a procedure that survives this red run is
protected by something other than capability scoping, which matters if that something is ever
refactored:

- 8 `AdminService` procedures, refused by `authorizeServiceAccountProcedure`'s unconditional "no
  service account is ever app-admin" rule.
- `ServiceAccountService/CreateServiceAccount`, refused because a machine may not issue
  credentials at all (see the R5 note below) — a check that runs before capability is consulted.


### 2026-08-22 — what fixing the review's findings taught us

- **The fix for "callers can choose X" is usually to stop accepting X, not to validate it.**
  Tenant identity was free-form request input with nothing to check it against, because no
  org→tenant mapping existed. Adding validation would have required inventing that mapping anyway;
  once it existed, the field had no reason to be on the request at all. It is now **reserved** in
  the proto, so the value cannot reach the server. A validated field you still accept is one
  refactor away from being accepted again.
- **A guard that fires before another guard hides it from that guard's red run.** Disabling
  capability scoping flips 25 of 34 procedures; the 9 survivors are held by *earlier* gates
  (8 by "no service account is ever app-admin", 1 by the new "a machine may not issue
  credentials"). Enumerating them matters: a procedure that survives a red run is protected by
  something other than the control under test, and that something might be refactored away by
  someone who believes capability scoping covers it.
- **Audit the actor, not the endpoint.** An agent's proposal left no trace because the endpoints
  it composes are reads. Auditing those endpoints unconditionally would have logged every
  keystroke of UI validation — a trail nobody reads is not an audit trail. Recording only when
  the caller is a machine identity gets R6's property without that cost, because "who is calling"
  was the distinction that mattered all along.
- **A redaction with a pointer receiver has a seam.** `Client.String` redacted the token for
  `*Client` but not for a dereferenced `Client`, which `fmt.Sprintf("%+v", *c)` produces by
  accident. Value receivers cover both. The existing test passed because it only ever formatted
  the pointer — a test that exercises one of two shapes proves one of two shapes.

## 11. Open decisions

1. ~~Gateway ownership~~ — **resolved, see D8.**
2. ~~Route prefix format~~ — **resolved, see D9** (both formats supported; slug+suffix is the
   default).
3. ~~Beacon ingest placement~~ — **resolved**: inside `internal/agentapi`
   (`beacon_handler.go`), same trust boundary and credential as `collector.v1`, reusing
   `verifyBasicAuth` directly rather than a second auth implementation. Not a Connect RPC
   handler, though — Prometheus remote_write is snappy-compressed protobuf over plain HTTP,
   not something a `collector.v1` RPC method could carry — so it is mounted as a plain
   `net/http` route (`POST /beacon/v1/write`) at the router root, next to the Connect mount
   rather than inside it or inside the session-authenticated `/api` group.
4. ~~Receiver-tier tenancy granularity~~ — **resolved for OTLP by D10** (one shared listener,
   gateway-injected tenancy). Remains open only for Faro, and only if Faro is ever built:
   the shard size in D10 option 3 must be measured against the Kubernetes Service object
   ceiling rather than assumed.
5. **Beacon retention.** How long per-instance inventory is kept, and whether history (not
   just current state) is worth storing for fleet drift analysis.

6. **W9 publish target.** Does the generated chart values file go to the same repo link a
   cluster already uses for pipelines, or a separate deployment repo?
7. **W9 ownership after generation.** The regenerate-and-PR-on-bump loop assumes Shepherd owns
   the file; a downloaded file cannot be owned. Likely: ownership is a property of the delivery
   mode, but say so explicitly.
8. **W10 team scope.** Are teams org-scoped or cross-org? Cross-org is a real ask from platform
   teams that operate every tenant, and it changes the ownership model.
9. **W10 ownership granularity.** Per pipeline, per folder/label, or per matcher scope?
10. **W11 proposal shape.** Does "propose" create a revision, a git PR, or a distinct proposal
    object with its own lifecycle? And should a simulation be mandatory before a proposal is
    acceptable — i.e. must the agent show its work?

> **SCIM is an explicit non-goal.** SCIM exists to close the gap where an app holds identities
> the directory does not control. Shepherd is OIDC-only (plus break-glass local admin), so a
> terminated user fails at the IdP's front door — there is no local account to linger, and the
> residual exposure is an active session bounded by `session_ttl`. If revocation latency ever
> becomes a hard requirement, the cheap fix is re-validating group claims during session checks,
> not implementing a SCIM server. (Grafana's SCIM is also SAML-only, which we do not speak.)
