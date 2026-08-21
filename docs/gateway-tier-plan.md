# Gateway tier, beacon, and tenant routing — multi-session implementation plan

> Status: **in progress — W1 and W3 done, W2 built (PR open); R1 review gate is next, before W4.** This document is the execution plan and
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
  discards. Rate-limited per instance, body-size capped.

### D7 — Grafana integration is optional enrichment, scoped to verification

An optional Grafana service-account token lets Shepherd answer the question its story currently
stops short of: *did the data actually arrive?* Querying the destination (`/api/ds/query`) after a
pipeline ships is the same principle as this repo's existing "`alloy validate` is not `alloy run`"
standard — assert the observable consequence — extended past the collector into production.

Bounded deliberately: **verification first**, destination import (endpoints and types; Grafana will
not hand back datasource secrets) if asked, deep links into Explore *which need no token at all* and
should exist regardless, and **no dashboard or alert-rule management**. Minimum token scope,
documented. Shepherd must keep working with no Grafana configured.

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
| **W4** | Receiver tier + tenant routes | W1, W3 | Per-tenant receiver Alloy (pipelines with `role=receiver`) for OTLP and Faro, with routes rendered per tenant/app. First infrastructure Shepherd manages directly, in our own cluster. |
| **W5** | Beacon + outcome verification | — (W1 makes it more useful) | Ingest endpoint in `agentapi` (D6), baseline pipeline, inventory storage + expiry, and the fleet-health surface it unlocks. **Plus the other half of the same question**: an optional Grafana service-account token so Shepherd can query the destination and confirm data actually arrived. The beacon proves the collector runs what we think; the query proves the data landed — neither is sufficient alone, and building them separately produces two partial answers. |
| **W6** | Three-way reconciliation | W1, W5 | Reconcile **declared** (attributes) vs **served** (our pipelines' signals) vs **observed** (beacon inventory). Contradictions surface as findings — this is what catches a BYO logs collector actually running `prometheus.scrape`. |
| **W7** | Onboarding artifacts | W4 | "Connect an app": render endpoint+headers+tenant into Lambda env, Terraform/SAM/CDK, container/k8s env, Faro web snippet, SDK inits. Golden-tested; every emitted endpoint must resolve to a really-rendered route. |
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
| **R1** | After W3, before W4 | Route/security model: URL-as-identifier caveat documented and not overstated, rate limits, origin allowlist, what a leaked prefix does and does not grant |
| **R2** | After W5 ingest lands | Data review of the beacon: exactly what is stored, retention/expiry, proof no config text or secret is retained, ingest abuse surface |
| **R3** | Before the receiver tier defaults on | Same bar F5 had to clear: containment, blast radius, and a documented off-switch that is itself tested |
| **R4** | Every session, at the end | No doc claims a control that is not wired. This repo has shipped that failure once (claimed CI gates that ran nowhere); the ledger entry and the code must agree |
| **R5** | Before W10's scoped write ships | Permission model review: does the team/ownership model actually deny what it claims, and is every write path covered rather than one chokepoint (G11, G12)? |
| **R6** | Before the MCP server is reachable by any non-human | Agent-actor review: capability scope, rate/budget limits, and that no apply path exists — plus how a proposal is attributed when the human on whose behalf it acts is absent |

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
| W2 destination templates + tenant bindings | proposed | — | Schema change; migration required |
| W3 Gateway API foundation | **done** | G1 ✅, G2 ✅, G3 ✅, G4 ✅ | Pin + conformance facts verified against the released CRDs (2026-08-21): at the v1.4 floor the Standard channel carries `URLRewrite`/`ReplacePrefixMatch` but **no CORS filter** (it reaches Standard only in v1.6.1), confirming D2. `make check-gateway-pin` (G1) and `internal/gateway.CheckSupport` (G2/D3) both red-run proven. `RenderHTTPRoute` emits a typed Gateway API v1 `HTTPRoute` that rewrites the tenant prefix and **sets** (never appends) `X-Scope-OrgID`. G3/G4 proven in the kind suite against a real controller: the backend observed `path="/v1/traces" X-Scope-OrgID="acme"` while the client sent `"evil-client-value"`, tenant B's prefix arrived tagged `globex` despite the client claiming `acme`, and the kill probe confirms removing the filter lets the client value pass straight through. |
| W4 receiver tier + tenant routes | proposed | G3, G4 · R1, R3 | |
| W5 beacon ingest + inventory | proposed | G5 · R2 | |
| W6 three-way reconciliation | proposed | G6 | |
| W7 onboarding artifacts | proposed | G7 | |
| W8 wizard catalog fan-out | proposed | G6 | See the wizard catalog in the session notes; W1 first |
| W9 chart values generator | proposed | G8, G9, G10 | BYO onboarding; independent of the gateway chain. Delivery is both publish-to-repo (via existing GitOps) and plain download |
| W10 teams + scoped identity | proposed | G11, G12, G13 · R5 | Blocks W11. SCIM is an explicit non-goal — see the note in §11 |
| W11 agent interface (MCP) | proposed | G14, G15 · R6 | Read + propose only |

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
## 11. Open decisions

1. **Gateway ownership.** Does Shepherd render routes into a gateway the customer owns
   (Shepherd needs RBAC for `HTTPRoute` in that namespace), or does it own a gateway
   outright? Affects W3's permission model and R1.
2. **Route prefix format.** `/{kind}/{tenant}` with an opaque per-app token, or a signed
   prefix? Decides rotation mechanics and what a leaked URL grants.
3. **Beacon ingest placement.** Inside `agentapi` (same trust boundary, same credential,
   same interceptor — the current preference) or a separate listener for isolation.
4. **Receiver-tier tenancy granularity.** One Alloy per tenant, or one shared receiver with
   per-tenant pipelines? Cost versus blast-radius trade-off; R3 depends on the answer.
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
