# Whole-project review remediation (2026-10-22)

Status: **decided 2026-10-22; steps 1–7a shipped** (1–6, 4b–4d and 7a: #363, #364, #366, #367, #371,
#372, #379, #370, #373, #381) in the prerelease v0.19.0-rc.1; **the §6 external-evaluation fixes
shipped** in the prerelease v0.19.0-rc.2 (#374 via #385, #377 via #383, the beacon half of #378 via
#384, plus #386, #387, #388 and #393). **Open: step 7b (#362), #375, the role-enforcement half of
#378, #389 and #390.** Built in the §3 order, one PR per step, merged one at a
time. Line references are to `main` at f367c70 (v0.18.0).

## Problem

The first whole-project review ran on 2026-10-22: a two-axis review (Standards against `AGENTS.md`,
`web/AGENTS.md`, `e2e/AGENTS.md`, `CONTRIBUTING.md`; Spec against `docs/spec.md`) of the full tree,
plus a correctness review of #348. Every finding the reviewers raised was re-checked by a separate
sub-agent before it was decided; the verdicts are in §2. The decisions were made with the owner in
one session, finding by finding.

## 1. Decisions

| # | Finding | Verdict | Decision |
|---|---|---|---|
| R1 | #348 reverted #347's README fixes (graph-view bullet, `config.server.base_url`, unclaimed-cluster step, Team member row, notices path) | confirmed by diff | Restore, keeping #348's e2e-k8s wording and v0.18.0's chart pin |
| R2 | `rpc_gitops_test.go` seeds `serve_cache` with raw SQL (`RAW-SQL-OK`) although `UpsertServeCacheConditional` exists | confirmed | Use the sqlc query |
| R3 | Bare `return err` in `routeapply/kube.go`, `store/migrations.go`, `mgmtapi/rpc_destination.go` | confirmed | Wrap with `%w` |
| R4 | Served bytes never pass Stage 2: `serve.ComputeServed` runs Stage 1 only, `stage3CheckSet` validates without the D6 baseline; recompute failure is a Warn with no `audit_log` row (spec §6.3) and no metric on the eager path | partly true — the baseline is a fixed `%q` template, low practical risk | Baseline appended in `stage3CheckSet`; failure → Error log + `audit_log` row + metric on both paths |
| R5 | Nil schema: the lazy serve path serves without role enforcement, the eager path refuses | true, deliberate ("degrade the guard"), only reachable with a broken build | A schema that fails to load is fatal at boot |
| R6 | Serve-cache write + compare-and-swap duplicated in `agentapi/service.go` and `mgmtapi/rpc_pipeline.go`, already diverging (superseded handling, metrics, log levels) | confirmed | Shared `serve.Store`; `recomputeOrgCaches` moves out of `rpc_pipeline.go` |
| R7 | Lazy path matches on the request's `local_attributes`, eager on the last stored row (PR-8b) | true, deliberate | Document in spec §5/§6.3; no code change |
| R8 | Git sync applies a commit file by file, each in its own transaction (spec §8: "never partially apply a commit"); the dry-run checks each file against its siblings' old content | confirmed, not previously tracked | Dry-run the whole commit as one candidate set, write it in one transaction |
| R9 | `UnregisterCollector` has no org check; `unregistered_at` is never cleared and hides the instance and drops its `local_attributes` from org recompute while it keeps polling | partly true — any token can already rebind any instance id through `GetConfig` | Unregister scoped to the caller's org; `GetConfig` clears `unregistered_at` |
| R10 | `GetConfig`'s instance upsert rewrites `collector_id` on conflict before the org is resolved — any agent token can take over any (hostname-like) instance id | found while verifying R9 | Refuse a rebind across orgs |
| R11 | Agent OIDC self-claims clusters and returns `PermissionDenied` on a disallowed claim; spec §4.1/§7.3 still say tenancy comes only from admin claiming and unclaimed means an empty config | true; shipped, spec never amended | Amend the spec; code stays |
| R12 | `DeleteRepoLink` (#348): unlocked read used instead of the `FOR UPDATE` row; org-wide recompute after dirtying one collector; in-tx audit | true; first two cosmetic/wasteful, audit deliberate | Use the locked row, drop the org-wide recompute, keep in-tx audit |
| R13 | Roles: one model spelled four ways (`reader/orgeditor/orgadmin` — dead code; `RoleOrg*`; `admin/editor/viewer` in DB/proto/SPA; app admin as a flag); every switch has a `default`, so `exhaustive` sees none; service-account tier falls back to editor on an unknown value | confirmed | Full refactor (§4) |
| R14 | Service accounts have no viewer tier | confirmed | Add it |
| R15 | `GraphViewPage` `moduleOfInstance` is O(n²) per rebuild | confirmed | Build the lookups once per graph |
| R16 | `GitPage` link delete leaves the collector's `served-config` / `revisions` queries cached | confirmed | Invalidate them |
| R17 | Spec §7.2 "Viewers create nothing" vs §12 team write; `database.html` says Shepherd never calls the Kubernetes API; misplaced `SandboxRunPanel` comment | confirmed | Fix the text |

## 2. Out of review scope

The Standards review did not read `rpc_interceptor.go`, merge, validate, gitsync, or the web
components in depth; the Spec review did not reach spec §9–11, §13–22 or the LA-1, FS-1, git-provider
and docs-truth amendments. A later review should start there.

## 3. Order

Smallest blast radius first; PRs 5 and 6 each get a fresh adversarial review before merge.

1. R1 — README restore, this plan, ledger entries.
2. R2, R3, R15, R16, R17 (`database.html`, `SandboxRunPanel`).
3. Spec text: R11, R17 (§7.2), R7.
4. Serve path: R6, R4, R5, R12.
5. Agent identity: R9, R10.
6. Git sync atomic commit: R8.
7. a. Role refactor (R13), behaviour-identical. b. Service-account viewer tier (R14).

## 4. Role refactor (7a/7b)

Approved 2026-10-22 on the basis that there is no production deployment yet, so compatibility is
not a constraint:

- Canonical names `viewer` / `editor` / `admin` in Go, DB, proto, SPA and docs; `reader` and the
  `org-*` spellings go. `orgs.reader_group_id` → `viewer_group_id` (new migration; committed
  migrations are not edited).
- One typed Go role with no-`default` switches; `RoleOrg*` becomes a typed requirement built from
  it; the dead `RequireOrgAccess` / `RequireAppAdmin` and their `reader/orgeditor/orgadmin`
  vocabulary are deleted (spec §7.2 updated).
- **Proto change approved:** `Role` enum (`ROLE_VIEWER`, `ROLE_EDITOR`, `ROLE_ADMIN`) for
  `OrgMember`, `OrgMembership` (GetMe) and `ServiceAccount`, no string compatibility.
- App admin stays a separate flag (cross-org scope), typed into the requirement model in place of
  `RoleOrgAny` / `reqAppOrOrgAdmin`.
- Team write becomes an explicit grant ("team owner-write") in code and spec §7.2/§12; behaviour
  unchanged.
- An unknown service-account tier denies (today it falls back to editor).
- Tenant-route status (`active` / `deprecated`) gets the same treatment.
- 7a changes no effective permission; the existing authz tests are the proof. 7b adds the
  service-account viewer tier (migration widening the CHECK, UI, docs).
- The release carrying 7a needs a chart minor, an `UPGRADING.md` section and a `CHANGELOG.md` entry.

## 5. Design decisions for steps 4–7b (decided 2026-10-22)

**Step 4 — serve path (R4, R5, R6, R12).**
- One serve-cache write: `serve.Store` does the conditional upsert (`DirtySeq`, `Format: serve.Format`)
  and reports written vs superseded. Each path keeps its own superseded policy (lazy serves the
  re-read row or its own result; eager moves on). `recomputeOrgCaches` moves to its own file.
- Recompute failure, both paths: Error log, `shepherd_serve_recompute_failures_total`, previous
  entry kept, and an `audit_log` row once per (collector, dirty_seq) — `serve_cache.last_failed_seq`
  advanced by compare-and-swap. Row: actor `serve`, actor_type `system`, the collector's org,
  resource `collector`/<id>, action `serve.recompute_failed`, detail `{dirty_seq, path, error}`.
- `stage3CheckSet` validates the served bytes: D6 baseline appended via the code `ComputeServed`
  uses, so `alloy validate` and PortShapes see what is served.
- The schema registry loads once in `server.New`; failure fails the boot. `Deps.EnforceRoles` and
  the nil-schema-unenforced branch are deleted — a nil schema is a programming error.
- `DeleteRepoLink` checks the org on the locked `GetRepoLinkForUpdate` row and drops the unlocked
  read; the in-transaction audit write stays. Dropping its org-wide eager recompute was
  proposed but is held: `GetServedConfig` reads `serve_cache` as-is, so an offline collector would
  show the deleted pipeline in the UI until it polls — an owner decision.

**Step 5 — agent identity (R9, R10).**
- `GetConfig` keeps upserting cluster + collector before org resolution (unclaimed discovery and
  OIDC auto-claim need them) but writes the instance row, its status and attributes only after the
  org check.
- Instances are keyed by `(collector_id, id)` (decided after the #370 review, replacing "move only
  within the org", which let the first writer squat an id across orgs): an instance never moves; a
  new cluster/role writes a new row and the old one ages out.
- `UnregisterCollector` marks only the rows in the caller's org (all rows for a static token or
  unbound OIDC); unknown and out-of-scope ids answer the same success. `GetConfig` clears
  `unregistered_at`.
- New metric `shepherd_agent_refused_total{reason=claim_denied|role_denied|unregister_denied}` plus a
  Warn log, rate-limited to the first per (instance id, reason) per five minutes.

**Step 6 — git sync atomic commit (R8).**
- #349 already writes in one transaction; the partial apply comes from dropping failing files. Any
  failing file (validation, ownership, name clash) now applies nothing, deletes included.
- A tripped prune guard applies nothing and marks a warning.
- gitsync's `stage3DryRun` covers the whole candidate set plus removed ids for the link's one
  collector, with role enforcement — a disallowed signal refuses the commit.
- Create/update audit rows record `commit`, `git_path`, `repo_link_id`.

**Step 7a — role refactor (R13).**
- buf-style `enum Role { ROLE_UNSPECIFIED = 0; ROLE_VIEWER = 1; ROLE_EDITOR = 2; ROLE_ADMIN = 3; }`
  in `proto/…/common.proto`; UNSPECIFIED is rejected on write and never granted.
- The SPA adopts the generated enums; tenant-route `status` and `apply_status` both become proto
  enums.

**Step 7b — service-account viewer (R14).**
- A service-account viewer gets exactly the human org-viewer procedures (no simulate); the default
  tier stays editor.

Migrations in steps 4, 7a and 7b are numbered by merge order.

## 6. External evaluation (2026-10-22)

A local lab evaluation of 0.18.0 (Alloy 1.18.1/1.20.1, local Kubernetes) was checked against main
4e2a3dd. Issues filed:

- #374 — start-up dependency.
- #375 — version-aware validation.
- #376 — Stage 3 scale: Stage 3 validated every collector in the org, once each. Step 4d validates
  only the collectors the old or new matchers select, once per distinct merged config (collector
  identity normalised to a placeholder), and exposes `config.validate.stage3_timeout` in the chart.
- #377 — restore keeps the enabled state.
- #378 — module pipelines. The beacon half is fixed (beacon keyed by `controller_path` +
  `controller_id`, nested modules rolled up to their `pipe_*` pipeline; no served-bytes change).
  The role-enforcement half (module instances counted as Unknown) stays open.

Notes, not issues:

- **Several tenant collectors on one cluster collapse into one logical collector.** A cluster
  belongs to one org and `(cluster, role)` is one collector, so collectors that report the same
  cluster name and role share one served config. The convention for now: give each such collector
  its own synthetic cluster attribute (a distinct cluster name per tenant) and document it.
  Whether one cluster can belong to several orgs is decided later.
- **No machine principal for org/token lifecycle automation.** Service accounts are never
  app-admin by design (spec §7.3a), IdP bearer tokens are not accepted by the management API, and
  a service-account write must name the creating human. Candidate: an app-admin-tier machine
  credential behind an explicit config flag, plus a per-service-account allowed `on_behalf_of`
  list.
- **Beacon URL and token env var names.** The D6 beacon's remote_write URL follows
  `server.base_url`, and the env vars the served baseline reads its credentials from are fixed as
  `SHEPHERD_AGENT_TOKEN_ID` / `SHEPHERD_AGENT_TOKEN_SECRET`; neither is documented. Document both
  now; an optional `beacon_url` can come later. Configurable env var names are out — they would
  change every collector's served bytes.
