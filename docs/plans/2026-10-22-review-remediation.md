# Whole-project review remediation (2026-10-22)

Status: **decided 2026-10-22, in progress.** Built in the §3 order, one PR per step, merged one at a
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
