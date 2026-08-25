# Full-application review — findings and fix plan (2026-08-25)

Five parallel reviews (auth/authz, chart/deployment, merge engine, API/data layer,
frontend). Every finding below was re-verified against the code before being
accepted; one agent finding was **rejected** as a documented design decision and
is recorded at the bottom so it is not re-litigated.

Phases are ordered by blast radius, not by effort. Each lands as its own commit.

---

## Phase 1 — CRITICAL: `helm upgrade` destroys the database

**F1.1 Hook resources are deleted and recreated on every upgrade.**
Helm: *"If no hook deletion policy annotation is specified, the
`before-hook-creation` behavior applies by default."* Omitting the annotation does
not opt out of deletion — it selects it. So with `cnpg.enabled`, every
`helm upgrade` deletes the live `Cluster` (CNPG cascades to PVCs), `initdb`
bootstraps an empty database, the migrate hook migrates it, and the upgrade
**reports success**. With `externalSecrets.enabled`, the `ExternalSecret` is
recreated, syncs once against fresh `Password` generators, and replaces the
non-rotatable encryption key.

- Fix: render the CNPG `Cluster`, the `ExternalSecret` and both `Password`
  generators **only when they do not already exist** (`lookup`). A hook absent
  from the manifest is not processed, so an existing one survives untouched.
  `lookup` returns empty under `helm template`, so rendering/testing is unaffected.
- Fix the three false comments asserting the opposite (`shepherd.bootstrapHookDeps`,
  `shepherd.migrateHookDeps`, `values.yaml`).
- `deploy/helm/chart_deps_test.go` asserts the *absence* of a delete policy with the
  comment "A delete policy here would drop the database between releases" — it
  enshrines the bug. Replace with an assertion on the guard.
- **F1.2** Add the kind test that would have caught it: install with cnpg+ESO,
  write a marker row, `helm upgrade`, assert the marker survives and the encryption
  key is byte-identical. Today `TestHelmChartIsRepeatable` asserts
  `schema_migrations >= 1`, which passes even if the database was dropped and
  re-migrated from scratch.

## Phase 2 — HIGH: cross-org access (four holes)

**F2.1 `authz.go` reader-floor fallback is not org-scoped.**
`ListCollectorIDsByGroupMembership` filters on group IDs only, so a viewer with a
collector assignment in *any* org clears the reader floor for *every* org. The
sibling team fallback three lines below is correctly org-scoped.
Fix: add the org filter to the query (join collectors→clusters) and a regression test.

**F2.2 FleetService by-id handlers have no ownership check.**
`getCollector`, `GetServedConfig`, `ListAssignments`, `CreateAssignment`,
`DeleteAssignment` act on ids without tying them to the request org — the
interceptor only proves a role in the org *named*. `GetCollectorOrgID` already
exists and is used by the agent API.
Fix: a `loadOwnedCollector` helper mirroring `loadPipeline`, applied to all five.

**F2.3 GitOps deletes have no ownership check.**
`DeleteCredential` / `DeleteRepoLink` delete by id and swallow the error, while
`TestCredential` in the same file does the check correctly.
Fix: load-and-verify, and stop discarding the delete error.

**F2.4 `CreateRepoLink` accepts cross-org `collector_id` / `credential_id`.**
Fix: verify both belong to the request org.

## Phase 3 — HIGH: authorization state and revocation

**F3.1 Revocation does not affect live sessions.** `is_app_admin` is frozen in the
session row and `disabled` is only checked at login, so a demoted or disabled
account keeps app-admin and org access for up to `session_ttl` (default 8h).
Fix: `DeleteSessionsForUser`, called on disable, on app-admin removal, on delete
and on password reset.

**F3.2 `UpdateOrg` omits the non-empty `admin_group_id` guard `CreateOrg` enforces**,
and empty group ids are not filtered from the Graph path — an org with
`admin_group_id: ""` would match a session carrying `""`.
Fix: mirror the guard; skip empty candidates in the group comparisons.

## Phase 4 — HIGH: served config correctness

**F4.1 Both mgmtapi merge paths drop git pipelines.** `stage3Check` and
`recomputeOrgCaches` use `ListEnabledPipelinesByOrg`, which omits
`repo_link_collector_id`, so every git pipeline matches nothing. The dry-run
validates the wrong merged config, and the eager recompute **writes the serve cache
with `dirty=false`**, so collectors are served a config with their GitOps pipelines
silently removed and the correct lazy path never runs again.
Fix: both paths use `ListEnabledPipelinesForMerge` and populate the field.

**F4.2 Matchers are never parsed at save.** One bad matcher on an enabled pipeline
aborts the whole `Assemble`, freezing config serving for the entire org; a
newly-claimed collector with no cache row is then served an **empty** config,
wiping what it was running.
Fix: parse at save; treat an unparsable matcher as "matches nothing" plus a visible
exclusion rather than failing assembly; never serve empty on recompute failure when
no prior cache exists.

**F4.3 `UpdatePipeline` skips stage 3** — the merged dry-run runs only at the enable
transition, not on edits to an already-enabled pipeline.

**F4.4 gitsync overwrites same-named UI pipelines.** `GetPipelineByOrgAndName` has no
`source` filter, so a repo file can replace a UI pipeline's contents while keeping
its matchers — deploying repo content fleet-wide, past the "git pipelines target only
their linked collector" model, on a Stage-1-only check.
Fix: scope the lookup to `source='git'`; treat a collision with a non-git pipeline as
a sync error.

## Phase 5 — HIGH/MEDIUM: frontend

**F5.1 `useCanWrite` locks editors and team members out.** It returns true only for
`role === 'admin'`, so the editor role shipped in v0.3.0 is unusable through the UI
and the docs claim otherwise.

**F5.2 `javascript:` destination URLs reach an `href`**, with no scheme validation on
either side, and `new URL(d.url).host` throws during render for anything unparseable
that reached the API directly.

**F5.3 Pipeline editor discards unsaved edits** when a refetch replaces `pipeline`
(window focus, 30s staleTime). No navigation guard anywhere.

**F5.4 Errors render as empty states** on Audit, Git, Destinations, Orgs, Clusters,
Tokens, Collectors, Overview — a viewer denied the audit log is told "No audit
entries yet."

**F5.5 Destination delete has no confirmation**, alone among destructive actions.

**F5.6 `orgs[0]` instead of the selected org** in GraphViewPage and BottomDrawer,
plus unhandled rejections there.

## Phase 6 — MEDIUM: chart and release hardening

- **F6.1** `--reuse-values` upgrades crash on nil for recently-added keys; use the
  `((.Values.x).y)` idiom the chart already uses elsewhere.
- **F6.2** `externalSecrets.enabled` without `cnpg.enabled` leaves no database URL and
  the guards block `existingSecret`; fail loudly, or let `existingSecret` coexist.
- **F6.3** `networkPolicy.enabled` blocks the metrics port, breaking the ServiceMonitor
  the same values file recommends.
- **F6.4** HPA and `spec.replicas` fight on every upgrade.
- **F6.5** Release workflow publishes images and the GitHub release *before* its own
  consistency guards run; `helm show chart` failure is indistinguishable from
  "not published"; no concurrency group.
- **F6.6** Deployment lacks `seccompProfile`/`runAsGroup`; main ServiceAccount
  automounts a token it never uses.

## Phase 7 — LOW and test gaps

- OIDC nonce; unbounded list endpoints; `values.yaml` prose that describes a default
  of 3 above `instances: 2`; `encryptionKey.length` knob that breaks Shepherd if changed.
- Modals have no Escape handler and no focus trap.
- CodeMirror diagnostics frozen at mount (stale closure).
- `draft.ts` is dead code and `draft.test.ts` tests it — the visual builder has no
  draft persistence at all.
- The `orgEditor` persona exists and is never used by any spec — the untested role is
  exactly the one F5.1 breaks.
- `protected-routes.spec.ts`'s "completeness guard" only checks the manifest type, and
  four real routes are missing from the manifest.
- The Playwright mock enforces authz only for OIDC and UserService, so persona
  assertions on Teams/Pipelines/Destinations/GitOps/Audit test UI hiding only.

---

## Rejected finding

**Global agent tokens are NOT a defect.** An agent flagged as CRITICAL that any token
can fetch any org's config by naming a cluster. The mechanics are real, but
`docs/spec.md:428` states: *"v1 tokens are global-authN only; tenancy comes from
cluster claiming."* It is a documented v1 limitation. Worth revisiting as a design
change, not as a bug fix.

---

## Outcome (2026-08-25)

All seven phases landed across four commits. Two findings were proven by
deleting the fix and watching the failure, rather than by reasoning:

- **F1.1** — with the existence guard removed, `helm upgrade` hung for 916s and
  failed with `Deployment/shepherd not ready ... Available: 0/1`: the
  application could not start because its database had been deleted underneath
  it. With the guard, the same upgrade completes in about five seconds and both
  a marker row and the encryption key survive. The prediction in this document
  was that the upgrade would report success; the observed behaviour is that it
  fails outright, which is recorded in the test.
- **F5.1** — reverting `useCanWrite` fails exactly the two editor-authoring
  specs while the viewer control still passes, confirming the widening is
  correct in both directions.

The cross-org and mock-authz fixes were kill-probed the same way.

### Deliberately not built

- **Team-scoped write in the UI.** A team member may write only the pipelines
  their team owns, and the client cannot derive ownership from `/api/me`. They
  are treated as viewers until a per-pipeline capability exists on the wire;
  widening `useCanWrite` to them would offer actions that fail for most
  pipelines.
- **Visual builder draft restore.** `draft.ts` implements IndexedDB save/load
  and nothing imports it. Restoring a draft well needs a restore-or-discard
  decision that is a product question, not a defect fix. A `beforeunload`
  warning now prevents the worst case (refresh or closed tab losing an hour of
  wiring); the rest is left explicitly unbuilt rather than half-built.
- **Unbounded list endpoints.** Real but low: every list is already org-scoped,
  so the blast radius is one tenant's own data. Adding a server-side ceiling
  touches every list query and every caller, and is better done as one
  deliberate pagination change than as a scattering of LIMITs.
- **OIDC nonce.** Defence in depth on top of authorization-code + PKCE + state,
  all of which are present and correct. Worth adding; not worth folding into a
  change this size, where it would be the only untested auth-flow edit.
