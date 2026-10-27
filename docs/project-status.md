# Shepherd — project ledger

> **Active work is tracked on the GitHub Project board, not here:**
> **https://github.com/users/procoduck/projects/1** ("Shepherd Implementation"). The board is the
> working tracker — status, assignment, and what's next. This document stays the human-readable
> **verified-baseline snapshot** (what demonstrably works, at which release/run), refreshed at each
> release; day-to-day item status moves to the board. The outstanding-work sequence that seeded the
> board is archived at `docs/archive/plans/2026-09-16-outstanding-work.md` — every item in it has
> since shipped (issues #100–#118 are all closed; the last, B1, in v0.13.0). Open work is the open
> GitHub issues (bar Renovate's Dependency Dashboard, #87), each represented in §2–§4 below.
> The 2026-10-08 pre-release walkthrough's low-severity findings and nits (#296–#310) shipped
> fixed in v0.16.0; the parts of #301, #308 and #309 that needed a proto or served-config change
> were approved on 2026-10-12 and ship in v0.17.0 (#332, #333) — all three are closed.
>
> Baseline re-verified 2026-10-24 for v0.19.0 (commit `c2d8a13`, chart 0.23.0) from the CI,
> CodeQL, security-scan and e2e runs on main, not from a summary. v0.19.0 is the 2026-10-22
> review remediation (steps 1–6, 4b–4d and 7a, #363–#381) with signed releases (#380), first
> published as the prerelease **v0.19.0-rc.1** (chart 0.23.0-rc.1); the external evaluation's
> fixes and the tenancy work — restore keeping the enabled state (#383), the beacon reporting per
> pipeline (#384), collectors riding out a database outage (#385), org-bound agent tokens (#386),
> several tenants per organisation (#387), strict tenant mode and the config size and nesting
> limits (#388), and bounded visual render output (#393) — first published as **v0.19.0-rc.2**
> (chart 0.23.0-rc.2); and the rc.2 upgrade walkthrough's gaps (#395) and B-BEACON-SCRAPE, the
> beacon that never reported (#396).
>
> **v0.19.1** (chart 0.23.1, 2026-10-26) is a patch on top: beacon ingest hardening (#389, PR
> #402), the bounded in-browser visual preview (#400, PR #403) and Alloy v1.21.2 (PR #404). Its
> baseline is `cf235a6` (§1).
>
> **v0.20.0** (chart 0.24.0, 2026-10-27) ships the 0.20 queue: module-based pipelines reaching
> restricted roles (#378, PR #406), viewer-tier service accounts (#362, step 7b, PR #407), pinned
> `import.git` for strict orgs (#390, PR #408), version-aware validation (#375, PRs #409, #412),
> the grafana/k8s-monitoring `labelmap` rules in strict orgs (#410, PR #413), strict tenant mode
> checking more constructs at save time and re-checking stored pipelines when served (PRs #415,
> #416), and the two pre-release kind walkthroughs' fixes (PRs #411, #414, #417, #418). Its
> baseline is `a4d7733` (§1). Known follow-ups are in §4.
> Completed rounds live in `docs/archive/` — the history this ledger used to carry inline is
> `docs/archive/completed-2026-09-11.md`. Do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — verified baseline, open bugs, unbuilt features, open follow-ups |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/plans/` | dated per-PR implementation plans while their work is unreleased; a plan moves to `docs/archive/plans/` once it has shipped in a tag. Empty between releases; open now: `2026-10-27-v021-queue.md` (0.21, #420–#426). The last shipped, `2026-10-26-v020-queue.md` (0.19.1 and 0.20, #389–#418), is in `docs/archive/plans/` with every earlier plan |
| `docs/visual-builder-design-VB1.md` | visual builder design — M1–M8 built; §6.4 (S3) is the live spec for the sandbox feature (enabled by default in the Helm chart since v0.0.1) |
| `docs/reviews/` | **live decision records only**: `canvas-framework-evaluation.md` (the React Flow decision and the controlled-mode contract `CanvasPane` depends on). Closed reviews move to `docs/archive/reviews/` |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/git-provider-design.md` | GitOps provider-auth design; live at top level because Go source cites its § numbers |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/kind-test-environment-plan.md` | kind-based Kubernetes test environment (`make e2e-k8s`, weekly + path-filtered on qualifying PRs) plus §11 the reusable dev stack it shares pins with (`make dev-kind`, the Kubernetes flavour of `make dev`). Steps 1, 2, 4, 6 done; step 3 (full-values install, true previous-version upgrade spec) and step 5 (`NOTES.txt` CNI warning) still open, step 7 (Layer C, LGTM) not started — see its status header |
| `docs/gateway-tier-plan.md` | **all 11 workstreams shipped** as of v0.12.0; every review gate signed (R1, R2, R3, R5, R6). Kept live for its decisions (D1–D11), §9 step ledger and the open questions in §11 (beacon retention, team scope/ownership, proposal shape) |
| `docs/proofs/` | red–green proofs for shipped controls. Not archived: Go source and CI workflows cite these paths |
| `docs/archive/` | finished work, kept as the record of why things are the way they are |

---

## 1. Verified baseline (2026-10-27, v0.20.0; table from 2026-10-24, v0.19.0)

**v0.20.0:** the baseline commit is `a4d7733` (#418, the last merge before the v0.20.0 release
PR). Its push runs are CI 40431408354, CodeQL 40431408270, security-scan 40431408360 and E2E
40431408271, all green. The Kubernetes e2e last ran on #416's head `f001c0e` (run 40413570496,
green) and on #414's head `fcd3b0e` (run 40385897578, green); #417 and #418 touched no path it
filters on. Each 0.20 PR added red-first specs for its change (the PR descriptions list them).
Two kind walkthroughs with Playwright ran before the tag: on `51c8f81` (every 0.20 feature passed;
its findings were fixed in #413 and #414) and on a fresh cluster at `8d85aaa` (every strict-mode,
tenant, service-account, pinned-import and version item passed; two Reconciliation failures and
five small findings, fixed in #417 and #418). Both fixes were re-walked on the same cluster with
Playwright: #417 on `c384f92` (every item passed except a collector whose only pipeline was
dropped, which led to #418) and #418 on `a4d7733` (that case and its sibling case passed;
migration 0046 applied; no ERROR or non-JSON server log lines). The table below is the v0.19.0 evidence and stays valid for every surface the 0.20 PRs
did not touch.

**v0.19.1:** the baseline commit is `cf235a6` (#404, the last merge before the v0.19.1 release
PR). Its push runs are CI 40309974049, CodeQL 40309974071, security-scan 40309973911 and E2E
40309973888, all green. The Kubernetes e2e last ran on #404's head `20ed8ab` (run 40308794316,
green) and on #402's head `f5aaed3` (run 40297155063, green). #402 adds the beacon scope and collector-id specs
and #403 the renderTS caps (Vitest). The table below is the v0.19.0 evidence and stays valid for
every surface the three PRs did not touch.

**v0.19.0:** The baseline commit is `c2d8a13` (#396, the last merge before the v0.19.0 release PR); its CI,
CodeQL and security-scan runs (40082542789, 40082542767, 40082542755) are green, every CI job but
`generated-drift` ran there (#396 touched Go, compose and docs; the drift job last ran on
#395's proto and sqlc regenerate, `9103f11`, run 40080823596), and its E2E run on push,
40082542810, is green — the agent-protocol suite, now including "the served beacon baseline
reports observed state" (real Alloy → `/beacon/v1/write` → `GetReconciliation`). The sandbox
`e2e-sim` job is path-filtered and last ran on #396's head `8b90700`, run 40081450080 (green).
The previous releases: **v0.18.0** (tag on `f367c70`, release run 38769359463), the prereleases
**v0.19.0-rc.1** (run 39836585311) and **v0.19.0-rc.2** (run 40043204621), all green. Each row
cites the run that last exercised the surface — CI's `changes` gate skips the jobs a commit does
not touch — so the claim is checkable by run id rather than by trusting this table.

| Check | Where it ran | Result |
|---|---|---|
| Go build, vet, `govulncheck`, `go vet -tags e2ek8s ./e2e/k8s/` | CI `build` job, run 40082542789 (`c2d8a13`) | clean (Go 1.27.3) |
| `golangci-lint` + config verify, all ten `make guards`, `helm lint` (incl. the dev-kind values), `scripts/repocheck` (incl. the beacon-env agreement and `alloy_log_verdict` specs, #395) | CI `lint` + `guards` jobs, run 40082542789 (`c2d8a13`); `generated-drift` on `9103f11`, run 40080823596 | 0 issues |
| `go test ./...` with coverage (testcontainers Postgres), incl. the org-bound token, multi-tenant and strict-tenant specs (#386–#388), the outage-cache specs (#385), the nesting and render bounds (#388, #393), the agent error-mapping interceptor and migration `0041` store spec (#395), and `TestRenderBaselinePipeline_Golden` (#396) | CI `test` job, run 40082542789 (`c2d8a13`) | green |
| `pnpm typecheck`, `biome check`, Vitest (unit + jsdom component) | CI `web` job, same run | clean |
| Mocked Playwright (`make test-ui`) | CI `test-ui` job, same run | green |
| `make smoke` + fullstack Playwright against the compose stack | CI `test-fullstack` job, same run | green |
| Compose e2e, agent protocol (`make e2e`), incl. the beacon end-to-end spec (#396) | `e2e.yml` on push to `c2d8a13`, run 40082542810 | green |
| Sandbox e2e (`make e2e-sim`) | `e2e.yml` `e2e-sim` job on #396's head `8b90700`, run 40081450080 | green |
| Kubernetes e2e, kind (`make e2e-k8s`) | `e2e-k8s.yml` on the pull-request heads of the v0.19.0 line, latest #396 (`728cd6b`, run 40081975423), before it `ef19bbd` (40050896909) and `80e2a3a` (40018212569) | green |
| CodeQL (actions, go, javascript-typescript, python; `security-extended`) | `codeql.yml` on `c2d8a13`, run 40082542767 | green |
| gitleaks, Trivy config + image scans, OpenSSF Scorecard | `security-scan.yml` on `c2d8a13`, run 40082542755 | green |
| Upgrade walkthrough 0.22.0 → 0.23.0-rc.2 on the kind dev stack | 2026-10-24: refused collectors shown as APPLIED, *In sync* without beacon data, "any" on unbound tokens, raw `42P10` text to Alloy — all fixed in #395; the empty beacon traced to B-BEACON-SCRAPE, fixed in #396 and confirmed live (`beacon_inventory`'s first rows, Reconciliation *In sync* with observed data) | fixed before the tag |
| Public docs review and screenshots | 2026-10-15, against v0.17.0 (`1c79ddc`): 21 docs pages, `site/index.html` and the README checked against the code (#347); all nine screenshots retaken on the kind dev stack plus the graph view (#346) | corrections merged; leftovers fixed in #348, which found the repo-link delete bug (fixed there) and B-GITSYNC-PRUNE (fixed in #349) |
| Manual walkthrough on the kind dev stack (Playwright MCP) | graph view, 2026-10-14: a real k8s-monitoring import (metrics config, 41 nodes, 1440×900); fixes in #344 | three mediums (zoom controls under the minimap, no zoom-out far enough, *Recreate* offered to viewers) and five lows fixed (#344); the image build context that crashed Docker during the session fixed (#343) |
| Manual walkthrough on the kind dev stack (Playwright MCP) | v0.16, 2026-10-12 on `b8ce639`; fixes in #329 | all v0.16 features pass; mediums/lows fixed (#329), the rolling-upgrade `(rev 0)` gap fixed (#330) |
| Rolling upgrade v0.15.0 → main on the kind dev stack | 2026-10-12, #330's head: DB at 31, v0.15.0 deployed with `(rev 0)` caches, then `make dev-kind-reload` | every polling collector serves `(rev N)` at format 1 |

### What demonstrably works end to end

Verified on the running stack and in the browser, not inferred:

- **Agent protocol** — real Alloy v1.21.2 agents register, poll and apply served config; status,
  hash and not-modified round-trip. Collector-token auth is a Connect request gate (v0.5.0): a
  bad credential is refused before the body is read.
- **Merge engine + validation gate** — served config carries both seeded pipelines, declare-wrapped,
  matchers resolving against real collector labels; role enforcement covers both the write-time
  and the live agent serve path (`internal/serve.ComputeServed`, one code path).
- **Management API** — `shepherd.mgmt.v1` Connect contract generated for Go and TypeScript (the
  legacy `/api` REST shim was removed in v0.11.0; `/api` serves only the schema artifacts);
  fail-closed per-procedure authz with the org-editor tier; service-account auth as a request gate.
- **All 21 SPA routes** (`web/src/routes/router.tsx`) — the walkthrough fullstack spec visits every
  route asserting no console errors, failed requests or blank pages; `route-guard.spec.ts` covers
  the client-side `requiredRole` guards (the server stays authoritative). Light mode follows the
  OS with a stored override.
- **Visual builder** — schema-driven palette (184 components, 314 named ports, 0 unnamed),
  draw.io-style connection dragging, delete/undo/redo, minimap, nested secret bindings, scalar
  fan-in with an Undo toast, IndexedDB draft autosave, save/load with matchers.
- **Pipeline editor** — CodeMirror with Alloy grammar, completion and server diagnostics; loaded
  on first editor mount (v0.5.0), not on first paint.
- **GitOps** — Gitea credential + repo link syncing for `basic`/`pat`/`ssh`/`github_app`; gitsync
  fails closed with a Stage-3 merge dry-run on every synced file.
- **Wizard, audit, overview, admin CRUD, local users, org switcher** — all functional.
- **S3 sandbox run** — a live run completes in ~20s with 21 captured series and 3/3 healthy
  components. Enabled by default in the Helm chart since v0.0.1 (both containment gates closed
  2026-08-21); the compose stacks keep their own opt-in `sim` profile.
- **Collector OIDC authentication (v0.8.0)** — a collector fetching a client-credentials access
  token is verified, grant-gated, and served its bound org's config, with the cluster auto-claimed;
  the whole path is proven end to end against a real token (`internal/agentapi/agent_oidc_e2e_test.go`,
  PR #95). Off by default; agent tokens keep working. Beacon write-back can use the same identity.
  Mode 2 (IdP-authoritative org) shipped in v0.9.0 (#129).

### Shipped since v0.8.0 — CI-verified, not yet re-walked in the browser

Each item is covered by the CI and release runs above and described in `CHANGELOG.md`; none has had
the manual end-to-end pass the list above records. Re-walk them on the next release.

- **v0.9.0** — tenant routes UI; service accounts UI with a per-service-account request rate limit
  (R6's last condition); `shepherd-mcp` in the release archives (W11); collector OIDC mode 2 and the
  collector-OIDC gate on the SSO page; collector inventory labels; editor **Format** / **Validate**;
  `shepherd_build_info`; the chart `kubeVersion` floor raised to 1.29; the `/api` REST shim
  deprecated with in-band headers.
- **v0.10.0** — structural graph diff for visual pipelines; experimental components as a per-org
  setting with a server-side render gate; a warnings channel on the wizard preview; the tenant-route
  create form asking for a gateway name in both modes.
- **v0.11.0** — the Reconciliation tab (#140, migration `0025`); attribute-based pipeline matching
  behind two per-org flags (#142/#144/#158, migration `0026`); the `/api` REST shim removed
  (#160/#161/#162).
- **v0.12.0** — the receiver tier (opt-in, R3 signed; #169–#172); Shepherd applying tenant routes
  as HTTPRoutes under receiver-gated RBAC (#175–#178, #180, migration `0027`); Connect an app
  (#111) and Connect a cluster with the G10 kind proof (#112).
- **v0.13.0** — Alloy v1.20.1 (#185); a rejected config stays FAILED instead of APPLIED (#186,
  migration `0028`, proof `docs/proofs/applied-status.md`); tenant-route apply under the chart's
  default two replicas, one reconciler at a time (#192).
- **v0.14.0** — collector status trustworthy: a failing collector never reads APPLIED (#197, #230),
  inactive derived at read time (#240, migration `0029`), no fleet reload on an unchanged config
  (#231); the gate refuses list-shape wires `alloy validate` accepts (#241) and reports a skipped
  stage (#235); chart server selectors exclude the sandbox (#239, chart 0.18.0); git sync single
  leader (#220); every write audited (#215); walkthrough UI fixes (#214–#228).
- **v0.15.0** — destination Secret auth rendered into wizard writers and proven through a real
  Alloy (#260); destination edits regenerate wizard pipelines, with a render fingerprint, Detach
  and Restore last wizard version (#263–#265, #291, migrations `0030`/`0031`); wizard output a
  running Alloy refused or that collected nothing (#287, #289, #293); APPLYING until the served
  config is loaded (#286); diagnostics in the user's coordinates (#316); stale-save refusal and
  per-pipeline `can_edit` (#317); Kubernetes 1.32 floor (#270); Go 1.27.2 (#315). Walked on kind
  2026-10-08 and 2026-10-09 before the tag.
- **v0.16.0** — destination tenant as `X-Scope-OrgID` with a cross-org guard on both sides (#277),
  TLS options read on the collector (#280), both proven through a real Alloy in `e2e-k8s` (#281),
  `rerender-destinations --all` (#282); the served header's real revisions with a one-time fleet
  reload (#323, migration `0032`); team owners in the visual builder (#324, proto `pipeline_id`);
  the rejected pipeline named (#327); the 2026-10-08 walkthrough lows (#325–#327). Walked on
  kind 2026-10-12 (fixes in #329, #330).
- **v0.17.0** — the read-only graph view reachable and honest for text, wizard and git pipelines
  (#341) and opening Alloy modules as groups with an elkjs layout (#342; proto
  `GraphNode.parent`); walked on kind 2026-10-14 against a k8s-monitoring import, fixes in #344 (the kind stack was
  rolled from #330's build — serve format 1 — to `fd63cdf`, and every polling collector then served
  the new header at format 2; the badge and the regenerated count were checked there too);
  the served header without "prod", serve format 2, one fleet reload (#333);
  `Pipeline.follows_wizard` and `Destination.regenerated_pipelines` (#332); six fuzz targets and
  the two bugs they found (#339); pnpm installed from a hash-locked manifest in the images (#340);
  code-scanning triage (#338). No migration.
- **v0.18.0** — the local sign-in per-IP throttle no longer keyed on client-chosen forwarding
  headers, with the opt-in `server.trusted_proxies` (#350); git sync deleting the pipelines of
  removed files behind the empty-tree and mass-delete guards, `RepoLink.sync_error` on the Git
  sync page (#349, migration `0034`, proto field 9); repository-link delete fixed, chart text
  and two UI labels (#348); the public docs reviewed and the screenshots retaken (#346, #347).
  Not walked in the browser before the tag.
- **v0.19.0-rc.1** (prerelease, chart 0.23.0-rc.1) — the 2026-10-22 review remediation: step 1
  README restore and plan (#363); step 2 standards and small fixes (#364); step 3 spec catch-up
  (#366); step 4 one serve-cache write, audited recompute failures, fatal schema (#367, migration
  `0035`); step 4b single-collector recompute after a repo-link delete (#371); step 4c one
  beacon-baseline input, serve format 3 (#372); step 4d Stage 3 on affected collectors only,
  `config.validate.stage3_timeout` (#379); step 5 instances bound per collector, request bounds
  (#370, migration `0036`); step 6 atomic git sync (#373); step 7a one role model, roles as API
  enums (#381, migration `0037`). Also signed tags and release attestations (#380) and Go 1.27.3
  (#365). Not walked in the browser.
- **v0.19.0-rc.2** (prerelease, chart 0.23.0-rc.2) — the external evaluation's fixes and the
  tenancy work: restore keeps the pipeline's enabled state (#383, #377); the beacon reports per
  pipeline, B-BEACON-KEY (#384, part of #378); collectors ride out a database outage —
  `Unavailable`, cached token verdicts and last served config, `agent.outage_cache_ttl` (#385,
  #374); agent tokens bound to an organisation with cluster/role allowlists, org-admin tokens,
  `may_claim`, `BindAgentToken`, unbound tokens refused (#386, migration `0038`); several tenants
  and tenant prefixes per organisation, `SetOrgTenantID` removed (#387, migration `0039`); strict
  tenant mode, `tenants.require_owned`, `shepherd admin tenant-preflight`, the 512 / 4096 / 1024
  nesting, chain and depth limits at Stage 1, the 1 MiB pipeline and 4 MiB request caps and
  `gitsync.max_file_bytes` ≤ 1 MiB (#388, migration `0040`); compose stacks bind their agent
  token before Alloy starts, `shepherd dev ensure-org` (#392); visual render output bounded,
  duplicate edges and labels/components over 256 bytes refused (#393, #391). Not walked in the
  browser.
- **v0.19.0** (chart 0.23.0) — everything in rc.1 and rc.2 above, plus: refused collectors
  shown as **REFUSED** with the reason on the Collectors list and collector page, Reconciliation
  saying *Observed state unknown* without beacon data, "— (unbound: refused)" for an unbound
  token's allowlists, no store error text to collectors (schema mismatch → retryable
  `Unavailable`) or in the management API's `mapError`, the beacon env vars in the kind, compose
  and e2e stacks, `dev-kind.sh status` telling refusals apart (#395, migration `0041`); the D6
  beacon reporting at all — the baseline scrapes Alloy's root `/metrics`, serve format 4,
  B-BEACON-SCRAPE (#396); the dev compose stack's `server.base_url` set to an address its Alloy
  agents can reach (release PR). The 0.22.0 → 0.23.0-rc.2 upgrade was walked on kind (#395, #396).
- **v0.19.1** (chart 0.23.1) — beacon ingest hardening: the beacon now drops and counts writes
  from unbound tokens, writes outside a token's or binding's cluster allowlist, OIDC writes naming
  another org's collector, and batches with a missing, invalid or mixed collector id
  (`shepherd_beacon_dropped_total`). Rebinding a bound token no longer grants `may_claim` (#389,
  PR #402). The in-browser visual preview is capped at 4 MiB / 800 diagnostics with 256-byte
  labels (#400, PR #403). Alloy v1.21.2, which clears the bundled-binary code-scanning alerts (PR #404).
  Not walked in the browser.
- **v0.20.0** (chart 0.24.0) — module instances carry their declare's signals and 24 single-signal
  OTel components are narrowed, so verbatim grafana/k8s-monitoring output reaches `metrics` and
  `logs` collectors (#378, PR #406); viewer-tier service accounts (#362, PR #407, migration
  `0042`); SHA-pinned `import.git` from an app-admin allowlist in strict orgs, served inlined as
  `import.string` (#390, PR #408, migration `0043`); validation against each collector's Alloy
  versions with schemas for every patch v1.18.0–v1.21.2 and `Collector.version_warning` (#375,
  PRs #409, #412, migration `0044`); the k8s-monitoring `labelmap` rules accepted in strict orgs
  with a served `__tenant_id__` drop (#410, PR #413); strict tenant mode checking more constructs
  at save time and re-checking stored pipelines when served, with exclusions in the header, the
  pipeline page, Reconciliation and the audit log (PRs #415, #416, migration `0045`); a refused
  instance visible beside healthy ones, `agent.refusal_visible_for` (PR #414); Reconciliation
  judging beacon rows against the instance's last report and saying "unknown" with nothing
  reporting (PRs #414, #417, #418, migration `0046`); audit, log-format and editor-warning fixes
  (PR #417). Walked on kind before the tag except the #417/#418 fixes (§1).

### History

The dated passes that used to sit here — the 2026-08-20 baseline, the 2026-08-21 W1 / sandbox
containment / health remediation passes, the 2026-08-22 gateway-tier build-out, the 2026-09-11
remediation with its D1–D14 decisions, and the v0.5.0 dependency and toolchain catch-up — are in
`docs/archive/completed-2026-09-11.md`, verbatim. `CHANGELOG.md` is the user-facing view.

- 2026-09-16 — v0.8.0 (collector OIDC), release run 35112915878.
- 2026-09-17 — v0.9.0 (Waves 1+2 of the outstanding-work plan plus the R6 rate limit, MCP archives
  and OIDC mode 2), chart 0.13.0.
- 2026-09-18 — v0.10.0 (graph diff, experimental components per org, wizard warnings), chart 0.14.0,
  release run 35334888511.
- 2026-09-28 — v0.11.0 (reconciliation, attribute-based matching, REST shim removed), chart 0.15.0,
  release run 36400183145.
- 2026-09-30 — v0.12.0 (receiver tier, tenant-route apply, Connect an app / a cluster), chart 0.16.0,
  release run 36688061602.
- 2026-09-30 — v0.13.0 (Alloy v1.20.1, rejected-config status, two-replica route apply), chart 0.17.0,
  release run 36700640070.
- 2026-10-01 — v0.14.0 (walkthrough round: trustworthy collector status, stricter gate, chart
  selectors), chart 0.18.0, release run 36850745268.
- 2026-10-09 — v0.15.0 (destination auth and re-render, two pre-release walkthroughs, Go 1.27.2),
  chart 0.19.0, release run 37954890404.
- 2026-10-12 — v0.16.0 (destination tenant and TLS, real revisions in the served header, team
  owners in the builder, walkthrough lows), chart 0.20.0, release run 38259974133.
- 2026-10-15 — v0.17.0 (graph view for imported pipelines incl. Alloy modules, served header
  without "prod", wizard-follow badge, fuzzing), chart 0.21.0, release run 38702024932 (tag on
  `1c79ddc`).
- 2026-10-15 — v0.18.0 (local sign-in throttle bypass fixed, git sync deletes removed files'
  pipelines, repository-link delete, docs review and screenshots), chart 0.22.0, release run
  38769359463.
- 2026-10-22 — v0.19.0-rc.1, prerelease (review remediation steps 1–6, 4b–4d, 7a; signed
  releases), chart 0.23.0-rc.1; release run recorded after the tag.
- 2026-10-23 — v0.19.0-rc.2, prerelease (org-bound agent tokens, several tenants per org, strict
  tenant mode, database-outage tolerance, config and render bounds), chart 0.23.0-rc.2; release
  run 40043204621 (rc.1: 39836585311; v0.18.0: 38769359463).
- 2026-10-24 — v0.19.0 (review remediation, org-bound agent tokens, several tenants per org,
  strict tenant mode, outage tolerance, config and render bounds, refused collectors visible, the
  beacon reporting), chart 0.23.0; release run recorded after the tag.
- 2026-10-26 — v0.19.1 (beacon ingest hardening, bounded in-browser preview, Alloy v1.21.2),
  chart 0.23.1, release run 40314944875.
- 2026-10-27 — v0.20.0 (viewer service accounts, version-aware validation, pinned `import.git`
  for strict orgs, k8s-monitoring pipelines under restricted roles and strict mode, the
  serve-time strict re-check, Reconciliation fixes), chart 0.24.0; release run recorded after the
  tag.

---

## 2. Open bugs

### B-CONTAIN-2 — `internal: true` does not deny the Docker host · **local dev only**

The bridge gateway is in-subnet, so on OrbStack every host-published port on the machine is
reachable from the sandbox — proven end to end. Docker Engine blocks it, so CI cannot catch it
either way.

This is an artifact of Docker bridge networking and does not exist in Kubernetes, which is the
production target. The Helm chart ships a default-deny NetworkPolicy on both Ingress and Egress
(plus `automountServiceAccountToken: false`, non-root, read-only rootfs, dropped capabilities), the
sandboxed Alloy is a child process of the simulator pod, and NetworkPolicy enforcement is verified
in a real cluster by the kind suite's Layer B probes (`e2e/k8s/simulator_containment_test.go`).
Compose stays a local-development convenience and this stays documented rather than fixed; a
developer who needs containment to be real locally uses `make dev-kind`, which installs Calico so
the chart's NetworkPolicy is enforced (`docs/kind-test-environment-plan.md` §11). B-CONTAIN-2 is
compose-only.

Fixed bugs (B-CONTAIN-1, B-CONCAT, B-STAGEORDER, F9-a) are in
`docs/archive/completed-2026-09-11.md` with their red-run evidence. B-VIEWER-CANVAS (#226, a
viewer's canvas and inspector stayed editable) shipped fixed in v0.15.0 (#256). B-GITSYNC-PRUNE
(a file removed from a linked repository kept its pipeline, contrary to `docs/spec.md` §10) ships
fixed in v0.18.0 (#349): a successful sync deletes them, behind an empty-tree guard and a
more-than-half-and-more-than-three guard (spec §10, "Removed files"); red-first specs in
`internal/gitsync/reconciler_internal_test.go`. The repository-link delete that failed with
`internal` once anything had synced (found in the 2026-10-15 docs review) ships fixed in v0.18.0
(#348). The per-IP local sign-in throttle bypass (chi's `middleware.RealIP` trusted
`True-Client-IP` / `X-Real-IP` / `X-Forwarded-For` from any client) ships fixed in v0.18.0
(#350); forwarding headers are read only from peers in `server.trusted_proxies`.

The three bugs the 2026-10-22 review found ship fixed in v0.19.0, first in the prerelease
v0.19.0-rc.1 (`docs/archive/plans/2026-10-22-review-remediation.md`):

- **B-GITSYNC-ATOMIC** (medium; R8, step 6, #373) — a git sync wrote the files of a commit that
  passed, dropped the failing ones, and under a prune guard still applied the commit's updates,
  contrary to spec §8. Any failing file or a tripped guard now applies nothing; the commit is
  dry-run once as a whole for the link's collector with role enforcement, refusing only errors the
  commit brings (pre-existing ones apply with a `warning`) and any check that could not complete;
  sync audit rows carry `commit`, `git_path`, `repo_link_id`; only the link's collector is
  recomputed. Red-first specs in `internal/gitsync/reconciler_internal_test.go` ("a commit
  applies atomically (R8)").
- **B-AGENT-INSTANCE** (medium; R9, R10, step 5, #370) — `GetConfig` could rebind any instance id
  across orgs and `UnregisterCollector` had no org check. Instances are keyed per collector
  (migration `0036`); the row is written only after the org check; a poll clears
  `unregistered_at`; an OIDC caller with an org unregisters only that org's rows (spec §4.1);
  refusals count in `shepherd_agent_refused_total`. A static agent token can still unregister
  any instance (it can already poll as any collector); the instance re-registers on its next
  poll. Specs in `internal/agentapi/instance_binding_test.go`.
- **B-SERVE-AUDIT** (low; R4, step 4, #367) — a failed recompute now logs at Error, counts
  `shepherd_serve_recompute_failures_total` on both paths and writes one `serve.recompute_failed`
  audit row per collector and generation (migration `0035`); Stage 3 validates the merged config
  with the D6 baseline appended.

Fixed in v0.19.0, first in the prerelease v0.19.0-rc.2:

- **B-BEACON-KEY** (medium; part of #378, #384) — `beacon.Project` keyed inventory on
  `controller_path` alone, which every declare-wrapped pipeline shares (`/remotecfg`), so all
  pipelines collapsed onto one row and reconciliation's observed side was always empty; the test
  fixtures used a shape Alloy never emits. The projection now joins `controller_path` +
  `controller_id`, keys by the first `pipe_*` segment and rolls nested module loaders up into
  their pipeline (unhealthy if any is); root and bootstrap loaders are dropped. No served-bytes
  change; rows stored under `/remotecfg` age out (5 min). Red-first specs in
  `internal/beacon/project_test.go`, `internal/agentapi/beacon_handler_test.go`,
  `internal/mgmtapi/rpc_reconcile_test.go`, plus an e2e spec over the real compose Alloy's
  `/metrics`. The role-enforcement half of #378 (module instances counted as Unknown) is open.
- **Restore switched a live pipeline off** (#377, #383) — a restore now keeps the enabled state.
- **Collectors crash-looped on restart during a database outage** (#374, #385) — see the
  accepted limits in §4.
- **Pathologically deep nesting could crash the server** (#388) — refused at Stage 1 by
  `internal/alloysyntax`, the one parse entry point (`TestNoDirectParserImports`).
- **A small visual graph could render to gigabytes** (#391, #393) — output bounded at 1 MiB,
  duplicate edges and labels/components over 256 bytes refused, diagnostics capped.

Fixed in v0.19.0 after rc.2:

- **B-BEACON-SCRAPE** (high; found live on kind 2026-10-24) — the D6 beacon had **never produced
  data** since it shipped (c276e9c). The served baseline scraped
  `prometheus.exporter.self.beacon.targets`, whose path
  (`/api/v0/component/remotecfg/prometheus.exporter.self.beacon/metrics`) Alloy answers with HTTP
  400 for an exporter delivered through remotecfg (upstream route bug), so only synthetic
  `up`/`scrape_*` series reached the keep rule and every beacon write was empty;
  `beacon_inventory` stayed empty and reconciliation's observed side was always unknown. Every
  test checked a layer short of the deployed artifact (render text, `Project` over hand-fed
  `/metrics`). The baseline now runs the exporter's target through `discovery.relabel` setting
  `__metrics_path__` to `/metrics` (Alloy's root registry, same in-memory listener, same
  `instance` label); serve format 4, so every beacon-enabled collector reloads once (approved
  2026-10-24). Red-first: `TestRenderBaselinePipeline_Golden` and the e2e spec "the served beacon
  baseline reports observed state" (real Alloy → `/beacon/v1/write` → `GetReconciliation`), which
  also needed the e2e stack's `server.base_url` set to an address Alloy can dial; the compose dev
  stack (`make dev`) gets the same fix in the v0.19.0 release PR (`SHEPHERD_SERVER_BASE_URL:
  http://shepherd:8080`); kind was unaffected. Only top-level remotecfg components hit the 400: an exporter inside a
  pipeline's `declare` (e.g. the Self Monitoring wizard's) answers 200 (checked live on kind), so
  managed pipelines were never affected. Upstream issue drafted, not filed. (#396)
- **Refused collectors looked healthy** (found in the 0.22.0 → 0.23.0-rc.2 kind upgrade
  walkthrough, #395) — every poll was refused (`token_unbound`) while the Collectors list and the
  collector page still showed APPLIED; Reconciliation said *In sync* with no beacon data; an
  unbound token's allowlists read "any"; Alloy logged an old pod's raw Postgres error (`SQLSTATE
  42P10`) during the rollout. Fixed: **REFUSED** with the reason (migration `0041`,
  `Collector.last_refused_reason` / `last_refused_at`), `GetReconciliationResponse.observed_known`,
  "— (unbound: refused)", and an innermost collector.v1 interceptor that maps schema mismatches to
  retryable `Unavailable` and anything else to `internal error` (the management API's `mapError`
  likewise).

**2026-10-08 pre-release walkthrough** (kind dev stack, Playwright MCP): four high and six medium
findings, all fixed in v0.15.0 — the builder save landing on stale data (H1), App
Observability scrape targets and log globs that collected nothing (H2, H3), the Database Metrics DSN
read from an environment variable nothing set (H4), and M1–M6 (Review-step errors, role-excluded
pipelines surfaced, actionable destination refusals, the kind agents' RBAC, editor diagnostics,
server-side form validation). PRs #288–#295 and follow-ups; `CHANGELOG.md` v0.15.0 has the list.
The low findings and nits are issues #296–#310, fixed in v0.16.0 (#323, #325–#327) except the
parts of #301, #308 and #309 that needed a proto or served-config change, approved 2026-10-12 and
shipped in v0.17.0: #332 (`Pipeline.follows_wizard`, `Destination.regenerated_pipelines`) and #333
(served header without "prod", serve format 2, single blank lines, visual header "N nodes"). A second pass on 2026-10-09 (`fcb8b17`) found
four lows (an untouched builder graph counted as dirty, never-connected collectors counted as live
matches, Self Monitoring's fixed role, no in-app 404), fixed in #318, plus the stale-tab overwrite
and team edit rights fixed in #317 and diagnostics one line off fixed in #316.

**2026-10-14 graph-view walkthrough** (kind dev stack, Playwright MCP, a real grafana/k8s-monitoring
metrics config imported as a pipeline, 41 nodes at 1440×900): three mediums (M1 zoom controls
unreachable under the minimap, M2 no zoom-out far enough to fit a chart-sized graph, M3 *Recreate
as visual pipeline* offered to viewers) and five lows (warning summary, module-instance link,
themed minimap, edge names, a refused `ListTeamMembers` on the audit page), all fixed in #344
before v0.17.0. The session also found the Docker image build context had grown to ~17 GB (agent
worktrees with their `node_modules`) and crashed the daemon; `.dockerignore` fixed in #343.

---

## 3. Unbuilt / gated features

### F-CONTRIB — collector detail does not show contributing pipelines · **low**

Served config is shown, but nothing links back to the pipelines that produced it, so there is no way
to get from "this collector runs X" to "because pipeline Y matched". The merge engine already knows
the contributing set.

All gateway-tier workstreams (W1–W11) and attribute-based matching (#139) have shipped — the last
of them in v0.12.0: the receiver tier (#109, opt-in, R3 signed), tenant-route apply, onboarding
artifacts (#111) and the chart-values generator with G10 (#112). See `docs/gateway-tier-plan.md` §9.

Closed features (F5 sandbox simulation, F-SIGNAL-SERVE) are in `docs/archive/completed-2026-09-11.md`.
F-REVISIONS closed — see `CHANGELOG.md` v0.6.0 "Pipelines — Shipped"; its plan is archived at
`docs/archive/plans/2026-09-11-f-revisions.md`.
F-DEST-TLS closed (#261): wizard writers send a destination's tenant as `X-Scope-OrgID`
(`tenant_id` on `loki.write`), refused when it is another org's (checked on the destination and
the org side under a per-tenant advisory lock), and render TLS options — a CA from a Secret or
ConfigMap (`ca.crt` or an overridden key), a client certificate from a `kubernetes.io/tls` Secret,
a server name, no `insecure_skip_verify` — read on the collector, proven end to end by
`e2e/k8s/destination_tls_test.go`. Stored tenants roll out through the operator's
`shepherd admin rerender-destinations --all` (`UPGRADING.md` 0.19.x → 0.20.0). Known limitation:
a missing CA key is not caught at load (documented). Step 1 (#273) shipped in v0.15.0, the rest
(#277, #280, #281, #282) in v0.16.0 (`CHANGELOG.md`); plan in
`docs/archive/plans/2026-10-06-destination-tenant-tls.md`.
F-DEST-AUTH closed (#229): wizard writers render a `basic_secret` / `oauth2_secret` destination's
auth from its Kubernetes Secret (`remote.kubernetes.secret`, key contract in `docs/spec.md` §11.4),
proven end to end by `e2e/k8s/destination_auth_test.go`. Shipped in v0.15.0 (`CHANGELOG.md`); plan in
`docs/archive/plans/2026-10-01-destination-auth.md`. Its follow-up #262 shipped with it: a destination update
re-renders the wizard pipelines that name it (same transaction, full gate), `DeleteDestination`'s
in-use guard matches the stored name, and `shepherd admin rerender-destinations` converts pre-#229
pipelines once (operator step, `UPGRADING.md` 0.18.x → 0.19.0).

---

## 4. Follow-ups

### Outage caches — accepted limits (#374, PR #385 review)

The in-memory outage caches (`internal/agentapi/outage.go`, spec §4.1/§7.3) have two known limits.
Both matter only while the database is unreachable.

- **Revoke/bind vs. concurrent fill.** `RevokeAgentToken` and `BindAgentToken` evict the verdict
  on their replica. A poll whose database lookup finished just before the change committed can
  still re-cache the old verdict and binding afterwards. That stale verdict is used only during an outage and expires after
  `agent.outage_cache_ttl`.
- **Identities: same bound, other replicas.** An OIDC caller's last-served key names its
  `agent_identities` binding as this replica last read it. `CreateAgentIdentity` /
  `DeleteAgentIdentity` forget it on the serving replica. Another replica, or a change made with
  `shepherd agent-identity`, can still answer under the old binding for at most
  `agent.outage_cache_ttl`, and only during an outage. A concurrent poll can also re-remember the
  old binding just after a forget, as with tokens.
- **Cache churn.** One valid token can cycle cluster/role names and churn the 10 000-entry
  last-served map, evicting other collectors' entries. Those collectors then get `Unavailable`
  during an outage, which they already ride out. Unclaimed (empty) configs are cached too.

### Decisions signed 2026-09-11

Product decisions taken in the same sign-off round as the gateway gates; each is the settled
answer and the ledger item it produced is below.

- **F-REVISIONS approved**: add revision `contents` to `PipelineRevision` and a `RestoreRevision`
  procedure (proto change approved).
- **Editor toolbar**: build **Format** (server-side `alloy fmt` via a new `FormatPipeline` RPC —
  proto change approved) and **Validate** buttons — "the editor needs those buttons for
  usability". **User menu** (avatar, role badges): struck from the spec.
- **Build both** the org-level experimental-components toggle with a server-side render gate,
  and the `shepherd_build_info` metric.
- **REST shim: schedule deprecation** — announce in the next changelog, add a deprecation
  header, remove a release later. Machine callers use Connect.
- **Product surfaces scheduled** for all four gated libraries: teams UI, reconciliation,
  onboarding artifacts, chart-values generator (+ G10).
- **gochecknoglobals: drop the vocabulary** — item closed, package-level vars stay allowed.
- Gateway gates: R1, R2 signed; R3 open with the receiver-tier build scheduled; R6 conditional on
  a per-service-account rate limit and a `pipeline.propose` audit event.

### Scheduled work (from the decisions above)

- [x] **F-REVISIONS**: `contents` on `PipelineRevision`, `RestoreRevision` RPC, the text diff
      view and Restore in the pipeline editor — shipped in v0.6.0, see `CHANGELOG.md`. Graph
      diff for visual pipelines is the remaining follow-up (below).
- [x] **Editor Format + Validate buttons** — shipped in v0.9.0.
- [x] **Experimental components as an org setting** — shipped in v0.10.0 (migration `0024`,
      server-side render gate).
- [x] **`shepherd_build_info` gauge** — shipped in v0.9.0.
- [x] **REST shim removal** — removed in v0.11.0 (#162) after its callers moved to Connect
      (#160/#161); `/api` now serves only the out-of-contract schema routes (`docs/spec.md` §12).
- [x] **Receiver tier build (R3)** — #109; shipped in v0.12.0, opt-in (R3 signed 2026-09-29).
- [x] **R6 conditions** — the per-service-account rate limit shipped in v0.9.0 (#130); the
      `pipeline.propose` audit row already existed. `shepherd-mcp` joined the release archives in
      v0.9.0 (#132).
- [x] **Tenant routes UI** (W4) — shipped in v0.9.0.
- [x] **Service-accounts UI** (W10 remainder) — shipped in v0.9.0.
- [x] **Reconciliation surface** (W6) — shipped in v0.11.0 (#140).
- [x] **Onboarding artifacts page** (W7) — #111, shipped in v0.12.0.
- [x] **Chart-values generator UI** (W9) + gate G10 in the kind suite — #112, shipped in v0.12.0.

### Smaller follow-ups

B1–B3 come from the v0.6.0 manual UI walkthrough (`docs/archive/plans/2026-09-14-walkthrough-fixes.md`,
§3 "Blocked") — all three have since shipped (B1 in v0.13.0, #115) — and two items from the kind dev stack's
first live bring-up (`docs/archive/plans/2026-09-14-kind-dev-stack.md`); every other finding from both
was closed in the same batches (`CHANGELOG.md` v0.7.0).

The 2026-10-22 whole-project review's findings were decided and sequenced in
`docs/archive/plans/2026-10-22-review-remediation.md`; everything it scheduled except the items
below ships in v0.19.0 — steps 1–6, 4b–4d and 7a (first in rc.1), the plan's §6 fixes (#374 via
#385, #377 via #383, the beacon half of #378 via #384) and the tenancy work (#386, #387, #388,
#393, first in rc.2), and the walkthrough fixes #395 and #396. Open, unless marked shipped:

- **Step 7b** — a service-account viewer tier, R14 (#362). **Shipped in v0.20.0** (PR #407,
  migration `0042`).
- **#375** — validation ignores each collector's Alloy version. **Shipped in v0.20.0**
  (PRs #409, #412, migration `0044`): Stage 3 and git sync's dry run check each collector's merged
  config against every active instance's Alloy schema (exact, else nearest older shipped schema
  with an approximation warning; pinned only for too-new, non-release or missing versions); a
  mismatch already served refuses only changes to the pipeline holding it. The agent API flags a
  mismatch that appears after the save on the collector (`Collector.version_warning`).
- **#378, role-enforcement half** — module-based (chart-generated) pipelines' instances count as
  Unknown and the pipeline is dropped from restricted roles. **Shipped in v0.20.0** (PR #406): an
  instance of a module declared in the pipeline or in a literal `import.string` carries its
  declare body's signals (lexical scope); `import.file`/`import.git`/`import.http` modules stay
  Unknown. Single-signal OTel components (the chart's `otelcol.exporter.prometheus`/`.loki`
  bridges among them) are narrowed by curated overlay `signals` overrides, so verbatim chart
  output reaches `metrics`/`logs` collectors.
- **#389** — beacon ingest hardening follow-ups. **Shipped in v0.19.1** (PR #402).
- **#390** — strict tenant mode: allow pinned external module imports. **Shipped in v0.20.0**
  (PR #408, migration `0043`).
- **#410** — strict tenant mode refused the grafana/k8s-monitoring chart's `labelmap` rules (1 in
  the logs output, 2 in metrics). **Shipped in v0.20.0** (PR #413): a strict org's served form drops
  `__tenant_id__` right after every labelmap that may write it (`validate.InsertTenantDrops`, inside
  `pinnedimport.Inline`); the lint accepts such a labelmap only when that drop follows it. The
  stored text is unchanged; non-strict orgs and pipelines without such a rule serve byte-identical.
  `pipeline_tenants` still records chart pipelines as incomplete (the index reads the stored text).
- **#400** — the in-browser visual preview (`web/src/visual/renderTS.ts`) has no label or output
  cap; the server's diagnostics are authoritative (#393 follow-up). **Shipped in v0.19.1** (PR #403).

Their order, releases (0.19.1: #389, #400; 0.20: #378, #362, #390, #375, both shipped) and design
decisions are in `docs/archive/plans/2026-10-26-v020-queue.md`.

**Known follow-ups from v0.20.0** (found in the pre-release walkthroughs and reviews, not fixed in
the release):

- [x] **Reconciliation after the sweep.** — shipped on main (PR #428, unreleased): each
      instance's last report is kept in `beacon_instance_reports` (migration `0048`), swept with
      the instance lifecycle, so the tab reads "nothing running". Original note: A collector whose only pipeline was disabled reads
      correctly (nothing running) while its beacon rows remain; once the sweeper has deleted them,
      the tab reads "unknown" rather than "nothing running", because no row is left to carry the
      instance's last report.
- [x] **The wizard preview shows "No problems" beside warnings.** — shipped on main (PR
      #428, unreleased): the preview reuses the editor's `EditorValidationStatus`. The pipeline editor counts
      warnings since PR #417; the wizard preview's summary does not yet.
- [x] **The trailing `// otelcol.exporter.loki` comment ends up after the inserted drop block.**
      — shipped on main (PR #428, unreleased): the block goes after a comment ending the
      exporter's closing line.
      In a strict org's served form, a `// otelcol.exporter.loki` comment that trails the exporter
      lands after the inserted `loki.process "shepherd_tenant_drop_<label>"` block instead of next
      to the exporter, so it reads as belonging to the wrong block. Cosmetic; served text only.
- [x] **Junk or flapping Alloy version strings are untested.** — shipped on main (PR #428,
      unreleased): specs added; they exposed unbounded versions in the audit row and fingerprint,
      an audit per re-check for a flapping unknown version, and NUL bytes failing as `Internal`,
      all fixed. The version-aware check is covered
      for released, pre-release, too-new and missing versions; malformed version strings, and an
      instance whose reported version keeps changing (beyond the once-a-minute re-check), have no
      spec.
- [ ] **The wizard preview never shows "alloy validate skipped".** It passes
      `alloyValidateSkipped={false}`: `RenderWizardResponse` carries no skipped stages, so a server
      without an Alloy binary still reads "No problems" there (needs a proto field; PR #428 review).

In rough priority order; closed items stay in place, marked with the release that shipped them:

- [x] **Bump Alloy to v1.21.x when it is released** — shipped in v0.19.1 (v1.21.2, PR #404):
      `make image-scan` reports 0 CRITICAL/HIGH in `usr/local/bin/alloy` in both images, so alerts
      140–157 and 159 should close on the first post-merge image scan. Original note
      (code-scanning alerts 140–157, 2026-10-13 triage):
  The vendored Alloy v1.20.1 is built with Go 1.26.7 and x/net 0.58.0; 18 Trivy alerts (HTTP/2 and
  HTTP/1 server DoS, crypto/tls ECH, html/template, one Windows-only) are reported against
  `usr/local/bin/alloy` and are not reachable — Shepherd runs only `alloy validate`/`fmt` (no
  listener, no network) and the simulator's `alloy run` is egress-contained. No v1.20.2 is
  coming; upstream `main` is already on Go 1.27.2 / x/net 0.61.0. It is a schema bump
  (`ALLOY_IMAGE`, `make schema`, overlay reconcile, `make generate-corpus`, re-stated census
  numbers). Alert 133 (Loki Ruler API path traversal, a Trivy pseudo-version mis-compare) does
  not close with it.

- [x] **UX polish from the 2026-09-30 walkthrough** (#212, closed 2026-10-01) — part shipped in
      v0.14.0 (#219/#223/#225/#227); the rest was split into #249–#253 (inline form errors, the
      post-login redirect, the builder toolbar, pipeline heading/enable/delete/owner team, nits),
      all closed and shipped in v0.15.0.

- [x] **`ValidatePipeline` stays open to org viewers** (decided 2026-09-28). Its Connect interceptor
      row is `RequireOrg(auth.RoleViewer)`: a viewer can validate pipeline text, which writes nothing apart
      from the `pipeline.propose` audit row for service-account callers. The removed REST shim had put
      it behind org editor; that stricter rule is not carried over. `role_matrix_test.go` pins the
      Connect behaviour.
- [x] **A collector's `APPLIED` status can mean "polled with the served hash", not "loaded it
      successfully."** Reproduced against a live Alloy v1.20.1 (it reports FAILED once with the
      rejected hash, then polls silently with it); fixed with option 1 — migration `0028`
      records the hash a status is for, and the clear-back skips a FAILED for the polled hash.
      Proof: `docs/proofs/applied-status.md`. #115.
- [x] **Wizards have no channel to say when they silently drop or add something** (B2) — a
      first-class warnings field on the render preview shipped in v0.10.0 (#134).
- [x] **`Chart.yaml` `kubeVersion` floor** (B3) — raised to 1.29 in v0.9.0 (#131).
- [ ] **`NOTES.txt` prints `https://` for every `route.hostnames` entry** although a route may be
      plain http (the kind dev stack's is). Cosmetic; fix with the next chart release.
- [ ] **A 30 s sandbox run against a 30 s scrape interval captures one scrape or none**, depending
      on where the scrape jitter lands — the first `make dev-kind` run showed 0 series and the next
      two 21. Containment and capture are fine; the run window versus the pipeline's own interval
      is the product question (a minimum window, or a first-scrape trigger).
- [x] **Graph diff for visual pipelines** — shipped in v0.10.0 (#137).
- [ ] **grpc held at v1.83.2.** v1.84.0 carries GO-2026-6443, which Shepherd's RPC code calls, so
      `make vulncheck` refuses it; the fix exists only in a `v1.85.0-dev` pseudo-version. Take the
      bump once v1.85.0 stable is released: `.github/dependabot.yml` ignores `>= 1.84.0, < 1.85.0`
      (#187), so the first stable 1.85 arrives as a normal Dependabot PR — drop the ignore then.
      The rest of each group ships as usual (#153, #188).
- [x] **`visual-drafts.spec.ts` flaked under load — it was a real bug.** The builder's draft
      autosave overwrote a pending draft on open (the schema-version stamp counted as an edit); the
      spec caught it whenever that save beat the restore check. Fixed on `main` (autosave now waits
      for the restore decision), with a deterministic regression spec.
- [x] **Bump Alloy for the 15 high CVEs in the bundled binary (done 2026-09-14, v1.19.2).**
      Trivy found 15 HIGH, unfixed-excluded CVEs in the v1.18.1 binary bundled into both images
      (built upstream with Go 1.26.5). v1.19.2 is built on a patched Go and carries 2, both in
      grpc (upstream). Bumped as a schema bump: `ALLOY_IMAGE`/`ALLOY_VERSION`, `make schema`,
      overlay reconciliation, the v1.18.1 artifact kept embedded for upgrade-review diffs.
- [ ] **Typed `Source` enum.** `pipelines.source` is a plain `string`-typed constant, not a
      distinct Go type — the `exhaustive` linter cannot check a switch over it for completeness the
      way it can for the four enums W2-S5 closed. (The role half is done: step 7a, #361, made
      `auth.Role` and `auth.Requirement` typed.)
- [ ] **Canvas keyboard wiring is partial.** Delete/undo/redo/copy-paste and re-focus-on-drop work
      (`web/src/visual/components/CanvasPane.tsx`); the a11y pass `docs/visual-builder-design-VB1.md`
      §8 calls for — keyboard node navigation, a focus ring on ports — is not built.
- [ ] **Nested `bindings[]` entries are unrenderable** — a known, accepted gap in
      `web/src/visual/bindings.ts` (W5-01/W5-02): a binding at a nested block's prop path writes
      correctly into `props` and renders, but the separate `GraphBinding`/`doc.bindings[]` channel
      stays flat-top-level-prop-only by design, not yet extended.
- [ ] **Kind suite, plan steps 3 and 5** (`docs/kind-test-environment-plan.md`): the full-values
      install and the true previous-version Helm upgrade spec (no longer blocked — charts 0.9.0
      through 0.18.0 are published), and the `NOTES.txt` CNI/NetworkPolicy warning. (G10 shipped
      with the chart-values UI in v0.12.0, #112.)
- [ ] Overlay entries scaffolded by `make schema` carry `needs_review: true` and need an editorial
      pass on the next Alloy bump.
- [ ] `go.mod` carries a vestigial `github.com/lib/pq` indirect line via testcontainers' own test
      dependency. No package we build or test imports it; `govulncheck` is clean.
- [ ] **`github.com/docker/docker` Dependabot alerts stay dismissed as unreachable, not fixed**
      (re-verified 2026-08-25: not in the build graph, no fix exists on that module path,
      `govulncheck` 0 reachable). Re-open and re-check if `golang-migrate`/`dktest` moves to
      `docker/docker/v29`.

Closed since the last pass, with the evidence in `docs/archive/completed-2026-09-11.md`: the React
19 / TypeScript 7 / Vite 8 migrations, request-gate authentication, the lazy editor chunk, the
`UpdateUser` `mapError` site (v0.4.0), the fullstack roles/rollout/wizard-commit/matcher-edit/
sandbox-run specs (all present under `web/tests/fullstack/`), the ginkgo CLI/module version
mismatch (both 2.32.1), "Helm containment asserted at template level only" (the kind suite's Layer
B probes dial from inside the simulator pod), the `UPGRADING.md` 0.9.x → 0.10.0 section, and the
`test-fullstack` Postgres-healthcheck flake (v0.2.1).

---

## 5. Explicitly out of scope (spec §19)

Webhook-triggered git sync; OpAMP; editing git-sourced pipelines in the UI; a
multi-wizard framework beyond the one wizard; Alloy version-matrix testing; front-channel SSO
logout; horizontal-scale coordination beyond stateless replicas + Postgres.

---

## 6. Test infrastructure reference

- **Backend**: Ginkgo v2 + Gomega; testcontainers Postgres (Docker required for `make test`);
  `make test` / `make e2e` (compose: postgres, mock-oauth2, mockmsft Graph+ADO mock with
  `/__fixture` injection, Gitea, shepherd, real Alloy); `make e2e-sim` (sandbox containment + run
  lifecycle; in CI as `e2e.yml`'s path-filtered `e2e-egress` job on PRs); `make e2e-k8s` (kind +
  Calico, `-tags e2ek8s`). `make dev-kind` is a dev stack, not a test target — `scripts/repocheck`
  verifies its script, manifests and values statically
- **Frontend**: Vitest units + jsdom component tests (`// @vitest-environment jsdom` per file);
  Playwright mocked suite (`web/tests/specs`, route interception, no MSW; canvas drags start from a
  settled layout via `web/tests/fixtures/canvas.ts`); Playwright fullstack suite against the real
  dev stack (`make test-fullstack`, `web/tests/fullstack`), including `walkthrough.spec.ts` which
  walks every route asserting no console errors, failed requests or blank pages
- **Cross-cutting**: shared Go↔TS golden corpus, read directly from `internal/visual/testdata/corpus/`
  by both sides (`web/src/visual/renderTS.test.ts` resolves the Go directory by relative path, so
  the two cannot drift by construction); the ten Makefile `guards`; `scripts/repocheck` (Ginkgo
  specs over the Makefile, workflows, `versions.env` pins, `renovate.json`/`dependabot.yml`, the
  chart's `values.yaml`, `scripts/dev-kind.sh` and `dev/kind/*`, `.goreleaser.yaml`,
  `scripts/build-docs.py`, `.gitignore`, web lockfiles)
- **CI** (`ci.yml`, SHA-pinned actions): `changes` gates the expensive jobs on their inputs; `lint`,
  `build` (incl. `govulncheck` and the `e2ek8s`-tagged vet), `guards` (incl. `helm lint` and
  repocheck), `generated-drift`, `test` (`make test-cover`, coverage artifact), `web`, `test-ui`,
  `test-fullstack` (incl. `make smoke`). `e2e.yml`'s `e2e` job runs on push to main, path-filtered, and on manual dispatch.
  `e2e-k8s.yml` weekly and on qualifying PRs. Scheduled: `schema-verify` and `govulncheck` weekly.
  `release.yml` on `v*` tags: verify job (incl. the Trivy image gate), goreleaser, provenance
  attestations, chart OCI push (refuses an appVersion/tag mismatch and an already-published
  chart version), then a report-only scan of the published images. `security-scan.yml` on every
  PR/push: gitleaks over the full history and Trivy misconfig over `deploy/`; Trivy over the two
  built images (gate on Shepherd's binary + base, report on the vendored Alloy binary) on every
  push and on PRs touching `deploy/`, the Go/npm manifests, `.goreleaser.yaml` or `.trivyignore`;
  weekly: the last released images; OpenSSF Scorecard weekly and on push to main. Branch
  protection on `main` applies to admins too and requires `guards`, `lint`, `build`, `test`, `web`,
  `test-ui`, `test-fullstack`, `CodeQL`, `secrets` and `config-scan`.
- **The repository is public, so standard-runner Actions minutes are not billed; GitHub still
  runs every job separately**, so a slow, noisy CI costs signal even when it costs no money.
  Three controls keep it in range — the sub-minute `changes` job gating every expensive `ci.yml`
  job on whether its own inputs moved (a docs-only PR runs only `changes` and `guards`; there is no
  `paths-ignore`, because a required check from a workflow that never starts would block the PR
  forever), path filters on `e2e.yml` and `e2e-k8s.yml`, and weekly rather than nightly
  scheduling for the kind suite. **Reduce redundant executions,
  never coverage**: every gate still runs when its inputs change, and everything runs locally.

### A standard this repo holds itself to

Two classes of defect recurred often enough to be worth naming:

1. **Silent-by-construction failures.** A dropped React Flow change type, a self-skipping spec, a
   containment key nobody probes — each reported success while covering nothing. Every control needs
   a test that *fails when the control is removed*; assert the observable consequence, not the
   configuration.
2. **`alloy validate` is not `alloy run`.** Validate accepts configs the real binary refuses at
   evaluation — that is how every committed golden came to describe a config Alloy cannot run (M13).
   Renderer output must be exercised against a running Alloy, not just validated.
