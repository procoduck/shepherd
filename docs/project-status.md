# Shepherd — project ledger

> **This is the single live status document.** Baseline established 2026-08-19; the ledger
> was then implemented (commits 381d876..2d9e8fa). Remaining open items are listed below. Completed design and
> progress documents have moved to `docs/archive/` (see `docs/archive/README.md`).
>
> Update the checkboxes here as work lands. Do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — baseline, open bugs, unbuilt features |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/visual-builder-design-VB1.md` | visual builder design; M7 (S3 sandbox) and M8 unbuilt |
| `docs/reviews/` | **deep review of the visual builder + schema pipeline (2026-08-19)** — start at `docs/reviews/README.md`; supersedes ad-hoc notes about codegen and canvas quality |
| `docs/git-provider-design.md` | **proposed**: standard-git GitOps with ADO service principals as one auth mode, tested against Gitea (F9) |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/proofs/` | red–green proofs for new work (older ones archived) |

---

## 1. Verified baseline (2026-08-19)

Everything in this section was confirmed by running it, not by reading a summary.

### Build and test health

- [x] `go build ./...`, `go vet ./...` — clean
- [x] `golangci-lint run ./...` — **0 issues** under the spec §20 config
- [x] `go test ./internal/...` — all packages green (testcontainers suites included)
- [x] `cd web && pnpm typecheck && pnpm test` — 97/97 vitest; `biome check src` clean
- [x] `pnpm exec playwright test` (mocked suite) — passing
- [x] `make e2e` — **14/14** specs against a real Alloy agent
- [x] `make release-snapshot` — green end-to-end (codegen hooks, SPA, 4 cross-builds, SBOMs, 2 images)
- [x] `make dev` — boots; `admin` / `admin` at http://localhost:8080

### What demonstrably works end-to-end

- [x] **Agent protocol**: three real Alloy v1.18.1 agents register, poll, and apply served
      config; status/hash/not-modified round-trip correctly
- [x] **Merge engine + validation gate**: served config carries both seeded pipelines,
      declare-wrapped, with matchers resolving against real collector labels
- [x] **Management API**: `shepherd.mgmt.v1` Connect contract (10 services, 47 procedures)
      generated for Go and TypeScript; every legacy REST route preserved as a wire-compatible
      shim for external integrations; fail-closed per-procedure authz
- [x] **Collectors UI**: live list with colored status, last-seen and version; detail page
      with full instance metadata and `local_attributes`
- [x] **Visual builder**: schema-driven palette (184 components), draw.io-style connection
      dragging, working save/load with matchers, design-token styling
- [x] **Schema pipeline**: `make schema` — edit `ALLOY_VERSION` in `deploy/versions.env`,
      run one command; overlay reconciliation scaffolds new components, prunes orphans
- [x] **Audit trail**: rows written on every mutation and returned by the API
- [x] All 12 SPA routes render with no console errors, no failed requests, no blank pages

---

## 2. Bugs — all fixed 2026-08-19 (kept for context)

Ordered by impact. Each was reproduced on the running stack unless noted.

### B1 — [FIXED 2026-08-19] Stale `FAILED` collector status never clears · **high**

A transient agent-side error (a DNS blip at startup is enough) sets
`collector_instances.remote_config_status = 'FAILED'` and it stays there permanently. The
agent recovers, keeps polling every 10s, `last_seen` advances — and the UI still shows the
collector red with a stale error string. Status is only rewritten when the agent volunteers
a new `RemoteConfigStatus`, which a healthy agent serving unchanged config may never do.

*Reproduced:* `prod-eu-1/metrics` sat at `FAILED` with
`err="unavailable: dial tcp: lookup shepherd ... no such host"` for the whole session while
Alloy logged zero errors and `last_seen` advanced every 10s.

*Same class as R2-H1*, which fixed only the sweeper's `inactive` marker
(`internal/store/queries/collector_instances.sql`, `internal/agentapi/service.go`). Needs a
decision on the clearing rule: treat a successful `GetConfig` whose served hash matches the
agent's applied hash as recovery, or expire a `FAILED` older than N successful polls.

### B2 — [FIXED 2026-08-19] Admin UI is read-only: core onboarding is impossible in the browser · **high**

The backend implements the whole admin surface, but the pages render tables and nothing else:

| Page | Missing |
|---|---|
| `AdminOrgsPage` | create / edit / delete org — **no affordance at all** |
| `AdminClustersPage` | claim / unclaim cluster; also hardcodes `unclaimed: true`, so claimed clusters are invisible |
| `AdminTokensPage` | revoke token (create exists) |
| `GitPage` | create repo link, create/delete ADO credential — empty state with no action |

Consequence: the documented onboarding flow (create org → claim cluster → assign groups)
cannot be completed through the UI at all. Spec §12 and §13.5 define all of it.

### B3 — [FIXED 2026-08-19] No organisation switcher · **high**

`web/src/hooks/useOrg.ts` is `me?.orgs?.[0]?.id ?? ''`. Every org-scoped page — pipelines,
destinations, git, audit, collectors — is permanently pinned to the alphabetically-first org.
An app admin who owns two orgs cannot reach the second one's data through the UI.

*Recorded as a `test.fail` marker* in `web/tests/fullstack/walkthrough.spec.ts` so it flags
the day a switcher lands. Needs: a switcher in the shell, the selection persisted
(localStorage or route param), and `useOrgId` reading from it.

### B4 — [FIXED 2026-08-19] Design tokens adopted only in the visual builder · **medium**

`web/src/pages/*.tsx` uses **162 raw `zinc-*` classes and 0 token classes**. The `@theme`
layer added during the visual-builder refinement (`--color-card`, `--color-panel`,
`--color-border`, `--color-accent`, …) is only used under `web/src/visual/`. The two halves
of the app will drift visually the first time a token value changes.

### B5 — [FIXED 2026-08-19] Group-assignment ("Access") management not wired · **medium**

`POST/DELETE /collectors/{id}/assignments` exist and are RBAC-gated; the collector detail
page has tab scaffolding but no working assignment editor, so readers cannot be granted
access to a collector from the UI.

### B6 — [FIXED 2026-08-19] `github.com/lib/pq` carries 7 known vulnerabilities · **low (reachable)**

Pulled in indirectly by `golang-migrate`'s `database/postgres` driver via `internal/store`.
`govulncheck` reports malicious-server panics and memory exhaustion, plus deprecated
`x/crypto/openpgp`. Low practical risk (the DB endpoint is ours). Fix: switch migrate to its
pgx driver, which removes the dependency entirely.

### B7 — [FIXED 2026-08-19] Documented spec-drift test does not exist · **low**

`docs/frontend-testing.md` describes a Vitest test that snapshots the mock route table
against the endpoint list in spec §12 and fails when an endpoint has no handler. No such
test is present, so mock/API drift is currently unguarded.

---

## 3. Features — implemented 2026-08-19 except F5 (kept for context)

### F1 — [DONE 2026-08-19] Wizard UI (spec §13.5, milestone 7) · **high**

Backend is complete: registry, `app-observability` schema, commit endpoint, Connect service.
`web/src/pages/WizardsPage.tsx` is an 8-line stub — no gallery, no stepper, no
`/wizards/app-observability` route. The Playwright spec self-skips when the start button is
absent, so CI stays green over the gap.

Also missing server-side: the wizard **render/preview** endpoint (spec §12) — only `commit` exists.

### F2 — [DONE 2026-08-19] Audit UI (spec §13.4) · **medium**

`web/src/pages/AuditPage.tsx` is an 8-line stub. The API works (verified: 4 rows for
`platform-org`) and returns actor/action/resource/timestamp; nothing renders them.

### F3 — [DONE 2026-08-19] Overview dashboard (spec §13.5) · **medium**

Collectors, active pipelines and clusters tiles are hardcoded `—`. Only the org count is real.

### F4 — [DONE 2026-08-19] Missing REST/RPC endpoints from spec §12 · **medium**

- `POST /api/orgs/{org}/ado-credentials/{id}/test` — verify credentials reach the repo
  (absorbed by **F9**, which redefines it as a git `ls-remote` reachability check)
- `POST /api/orgs/{org}/repo-links/{id}/sync` — force immediate sync
- `PUT/PATCH /api/orgs/{org}/ado-credentials/{id}` — update a credential
- wizard render/preview (see F1)

None of these exist in the proto contract either, so each needs an RPC + shim + UI.

### F5 — VB-1 M7: S3 sandbox simulation · [IMPLEMENTED 2026-08-20 — see §3a, MUST STAY DISABLED]

`docs/visual-builder-design-VB1.md` §6.4. Built on 2026-08-20 but its containment claim does not
hold; the feature is off by default and must stay off. See the F5 entry in §3a STILL OPEN and
`docs/reviews/s3-sandbox-security-findings.md`.

### F6 — [DONE 2026-08-19] VB-1 M8: hardening · **low**

Includes the deferred item: `wizard_state` is not persisted on pipeline `PUT`
(`UpdatePipelineParams` has no such field), so a visual pipeline edited through the text API
loses its graph.

### F9 — [DONE 2026-08-19] Standard-git GitOps with pluggable provider auth · **high**

**New requirement (2026-08-19).** GitOps must work against any standard git server, as broadly
as possible. Provider-specific work is confined to **authentication**: Azure DevOps needs Entra
service principals, GitHub needs GitHub Apps; everything else is ordinary git credentials.
Testing moves to a real **Gitea** instance instead of the hand-written ADO REST mock.

Today Shepherd never speaks git at all: `internal/ado/client.go` calls the Azure DevOps REST
API, there is no git library in `go.mod`, and provider concepts reach the schema
(`ado_credentials`, `repo_links.project`) and the wire contract
(`shepherd.mgmt.v1.AdoCredential`). So no other host works, and the e2e GitOps scenario proves
only that Shepherd talks to its own mock.

Design: `docs/git-provider-design.md`. Shape of the work:

- [ ] `internal/gitrepo` speaking real git over HTTPS/SSH via **go-git v6** (pure Go — no `git`
      binary needed in the distroless image); `ls-remote` for change detection, shallow
      in-memory clone for fetch
- [ ] Six auth strategies behind one interface: `none` · `basic` · `pat` · `ssh` · `ado_sp` ·
      `github_app`. `pat` alone covers Gitea/GitHub/GitLab/Bitbucket; `ado_sp` and
      `github_app` share a short-lived-token cache. `internal/ado` shrinks to just the Entra
      token provider
- [ ] **GitHub Apps**: RS256 JWT → installation access token, GitHub Enterprise Server via
      `api_base_url`
- [ ] **SSH deploy keys** with mandatory `known_hosts` verification (no accept-any mode)
- [ ] **Private CA trust** per credential (`ca_cert`), plus an explicit default-off
      `tls_insecure_skip_verify`; standard proxy env honoured
- [ ] **Resource limits**: max repo bytes (50 MiB default), max file bytes, max files, fetch
      timeout — enforced by a counting reader, not a post-hoc check
- [ ] Migration `0006`: `ado_credentials` → `git_credentials` with `kind` + `provider_config`
      JSONB; `repo_links` gains `repo_url` (backfilled) and drops `project`/`repository`
- [ ] Proto/service/UI rename `AdoCredential` → `GitCredential`; **breaking wire change**,
      justified only because nothing consumes the contract in production yet — re-verify
      before implementing
- [ ] Gitea in dev + e2e compose; scenario 5 rewritten to push real commits (finally making
      the update path from `0194541` testable end to end) and table-driven across auth kinds;
      `mockmsft` keeps only its Entra/Graph + token-endpoint role
- [ ] Implements the credential `test` endpoint from F4 as a real `ls-remote` reachability
      check, and the `GitPage` CRUD that B2 records as entirely missing

**Known implementation constraint** (verified against go-git v6 docs): the documented custom-TLS
mechanism is a *global* `transport.Register`, which cannot express per-credential CAs — the
package must build a transport per credential via the plumbing API. Do not paper over this with
a global registration; one repo's skip-verify would apply to every other repo.

Still open in the design: additional providers (AWS CodeCommit SigV4, GCP Source Repos — both
reachable today via `basic`), and pre-expiry warning for rotating GitHub App keys / ADO client
secrets.

### F7 — [DONE 2026-08-19] CI · **high (process)**

There is no `.github/` (or any pipeline definition). Everything — lint, tests, e2e,
`release-snapshot`, the `check-*` guards — runs only when someone runs it locally. The spec
assumes CI enforces the milestone gates.

### F8 — [DONE 2026-08-19] Remaining smaller gaps

- [ ] Health endpoint does not check DB connectivity or pending migrations
      (`internal/server/server.go:250`)
- [ ] Overlay entries scaffolded by `make schema` carry `needs_review: true` and need an
      editorial pass on the next Alloy bump
- [ ] `AGENTS.md` still contains two near-identical Docker image tables (scrub artifact),
      with disagreeing Go versions between them
- [ ] Stale comment: `internal/server/server.go:177` says "TODO milestone 6: serve embedded
      SPA" directly above the line that serves it

---

## 3a. STILL OPEN

### F-REVISIONS — revision diff and restore are not buildable yet · **medium**

`shepherd.mgmt.v1.PipelineRevision` carries only `revision`/`changed_by`/`changed_at`/
`change_note`. It does **not** carry the revision's contents, so a diff between two
revisions is impossible and restore cannot repopulate the editor — the Restore button
raises "Revision contents are not exposed by the API yet" and there is no
`RestoreRevision` RPC. The revision *list* works and is covered.

Needs `contents` on the proto message plus a `RestoreRevision` procedure before any UI
work. Two specs that pretended to cover this were removed (see `web/tests/specs/revisions.spec.ts`)
— they asserted the same locator as the list test and could never have failed.

### F-CONTRIB — collector detail does not show contributing pipelines · **low**

The served config is shown, but nothing links back to the pipelines that produced it, so
there is no way to get from "this collector is running X" to "because pipeline Y matched".
The merge engine already knows the contributing set. A spec named for this existed but
asserted only that a heading was visible; it was removed rather than left as false cover.


### B-MINIMAP — the canvas minimap draws no nodes · [FIXED 2026-08-19]

Found in the 2026-08-19 browser sanity pass: the minimap renders as an empty grey rectangle
however many nodes are on the canvas. React Flow gates `MiniMapNodes` on
`nodeHasDimensions(userNode)`, and because the canvas runs controlled, the node objects
`CanvasPane`'s `rfNodes` memo builds are all React Flow sees — they carry no
`measured`/`width`/`initialWidth`, so every node is filtered out.

Fixed as part of the controlled-mode contract work, not on its own — a spot fix was tried
first and broke connection dragging, because the wire gestures had come to depend on the
re-measure that a missing `measured` forced. The canvas now routes React Flow's whole change
stream through `applyNodeChanges`/`applyEdgeChanges` and reconciles its node array instead of
rebuilding it, so handle bounds are cached and `measured` is set the way the library intends.
`GraphViewPage` had the same defect and got the same treatment.

Full analysis: **`docs/reviews/canvas-framework-evaluation.md`**.

### B-SCHEMACACHE — `/api/schema/current` cached for a day · [FIXED 2026-08-19]

`GetCurrent` and the version-pinned `Get` shared `public, max-age=86400, immutable`. Only the
pinned endpoint earns that — it is content-addressed. `current` moves whenever the schema is
regenerated, so browsers kept serving a stale artifact from disk for 24h with no revalidation.

Symptom: the builder showed "Edges: 2" and drew none. The served schema had gained per-port
`prop`/`role` fields, but the page still read a cached copy without them, so every handle fell
back to a synthetic `p0`/`p1` index and no stored edge could resolve. `curl` saw the new shape
while the page saw the old one — which is what made this look like a renderer bug for a while.
`GetCurrent` now sends `no-cache`, keeping the ETag so unchanged schemas still 304.

This also accounted for two oddities noted earlier in the walkthrough: port labels appearing to
have vanished, and the toolbar/drawer problem counts disagreeing. Both were the stale schema.

### VB-REVIEW — visual builder is not usable end to end · **critical**

Three independent fresh-context reviews (2026-08-19) found the builder cannot currently produce
a working pipeline, and that the test suite cannot detect this because every layer validates
against hand-written fixture schemas whose port model is the inverse of the shipped artifact.
Measured: 0/9 corpus graphs match their goldens against the real schema, 8/9 goldens are
rejected by real `alloy validate`, 69.6% of the configuration surface is unreachable in the
inspector, nothing can be deleted on the canvas, and saved graphs do not round-trip.

Full detail and prioritised ordering: **`docs/reviews/README.md`**. Treat that as the work plan
for the visual builder; the F9-c and unnamed-port items below are subsumed by it.


### F5 — VB-1 M7: S3 sandbox simulation · **implemented 2026-08-20, MUST STAY DISABLED**

Built: the simulation transform (`internal/simulate/transform.go`), the `shepherd-simulator`
service (`cmd/shepherd-simulator`, `internal/simsvc`) with capture harness and synthetic sources,
the run API (`/simulate/runs`, migration 0007, `RunWorker`), the sandbox-run UI, and the `sim`
compose profile plus the `sandbox-sim` e2e scenario.

**The containment claim does not hold yet, so the feature is off by default and must stay off.**
`simulator.enabled` has no default (false) and both compose files default `SHEPHERD_SIM_ENABLED`
to false. Two critical holes: the secret sweep is type-driven and most credential-bearing
attributes in the artifact are typed `string`, not `secret` (147/290 measured at two levels,
518/718 measured deeper); and a graph naming an arbitrary host directly passes the transform,
real `alloy validate` and the endpoint allowlist, leaving Docker's internal network as the only
control — untested by execution.

Full list, with evidence: **`docs/reviews/s3-sandbox-security-findings.md`** (2 critical, 6 high,
5 medium, 4 low). Close the criticals and the highs before enabling this anywhere.

#### Original scope note (kept for context)

`docs/visual-builder-design-VB1.md` §6.4. Ephemeral Alloy runner with a capture harness,
graph rewrite (destinations → capture receivers, discovery → stubs, secrets dropped),
`/simulate/runs` endpoints, run UI, containment, and the `sandbox-sim` e2e profile.
Deliberately excluded from the 2026-08-19 implementation run: it executes user-authored
config and needs real security containment, so a rushed version is worse than none.
S1 (flow check) and S2 (relabel/log trace) are done and shipping.

### F9-a — ssh auth kind fails in the compose stack · **medium**

The `ssh` credential kind works in `internal/gitrepo`'s own suite, which performs real ssh
handshakes against a Gitea container and covers the positive case plus wrong-host-key and
wrong-passphrase negatives. In the e2e compose stack the same path fails with go-git's
"unable to find any valid known_hosts file", i.e. the per-credential `HostKeyCallback` is
not reaching the transport there even though `git_credentials.ssh_known_hosts` is populated
and the username is correct. The e2e case is `Skip`ped with that reason — it is left in
place, and host-key verification must NOT be relaxed to make it pass. Every other auth kind
(`pat`, `github_app`) passes end to end.

### F9-b — visual builder port identity is synthetic · [FIXED 2026-08-19]

Found in the browser walkthrough: opening the seeded `demo-visual` pipeline shows 3 nodes
but **draws none of its 2 edges**, and L1 reports the nodes as unwired
(`dangling_input ... input "p0"`, `output_nowhere ... output "p0"`).

Root cause is upstream of the UI: **the schema artifact carries no port names at all** —
0 of 225 ports across all 184 components have `prop`/`export`, because
`tools/alloy-schema-gen/extract.go` declares those fields but never populates them (it
builds ports from `metadata.AllTypesExported()`, which yields only types). Every handle
therefore falls back to the synthetic `p0`/`p1` ids from `portHandleId`, while any stored
graph references real Alloy names (`targets`, `forward_to`, `receiver`), so edges match
nothing and are silently dropped.

Note this is what R1-H1's fix actually did: it made rendering and validation agree, but on
synthetic ids — which hid the fact that the underlying data was missing. Consequences: no
saved graph round-trips faithfully, and codegen cannot map ports to real Alloy attribute
names by identity.

Fix spans: extractor must emit port names → regenerate the artifact (needs the network
clone) → overlay refresh → correct the corpus fixtures and the `demo-visual` seed.

### F9-c — one edge direction still not drawn · **medium**

After F9-b, handles carry real names (`targets`, `forward_to`, `receiver`, verified in the
DOM with no synthetic `p0`/`p1` left) and the forward edge renders. The reverse-direction
edge — `prometheus.remote_write.receiver` (an export, right side) into
`prometheus.scrape.forward_to` (an argument, left side) — is still not drawn even though
both handles exist with the correct types and positions. The store reports 2 edges and
React Flow renders 1. Needs DOM-level debugging of React Flow's edge resolution for a
right-to-left connection. The walkthrough spec asserts port naming and a non-zero edge
count rather than the exact count, so the guard holds without overclaiming.

### Smaller follow-ups

- [x] `make e2e` resets volumes before starting (2026-08-19)
- [x] e2e moved to a dedicated 18xxx/19xxx host port range; it now runs alongside a live
      dev stack (2026-08-19)
- [x] Canvas re-fits on graph load; pipeline deep links resolve across orgs (2026-08-19)
- [ ] 47 ports (otelcol, faro, beyla) still have no names — their inputs arrive through a
      nested `output` block rather than an attribute, so the struct-tag walk misses them.
      The corpus fixtures address these as `output.metrics` / `input.metrics`.
- [ ] CI wires lint/build/guards/test/web; the spec's fuller ordering (smoke,
      test-integration, test-ui, test-fullstack) is not yet wired — noted in `ci.yml`.
- [ ] `go.mod` still carries a vestigial `github.com/lib/pq` line via testcontainers' own
      test dependency. No package we build or test imports it (govulncheck is clean).
- [ ] Overlay entries scaffolded by `make schema` carry `needs_review: true` and need an
      editorial pass on the next Alloy bump.

## 4. Explicitly out of scope (spec §19)

Per-org agent tokens; webhook-triggered git sync; OpAMP; editing git-sourced pipelines in the
UI; a multi-wizard framework beyond the one wizard; Alloy version-matrix testing; front-channel
SSO logout; horizontal-scale coordination beyond stateless replicas + Postgres.

---

## 5. Test infrastructure reference

- **Backend**: Ginkgo v2 + Gomega (15 of 24 packages have suites); testcontainers Postgres for
  integration; `make test` / `make test-integration` / `make e2e` (compose: postgres,
  mock-oauth2, mockmsft Graph+ADO mock with `/__fixture` injection, shepherd, real Alloy)
- **Frontend**: Vitest units; Playwright mocked suite (`playwright.config.ts`, route
  interception — no MSW); Playwright fullstack suite against the real dev stack
  (`playwright.fullstack.config.ts`, `make test-fullstack`), including
  `walkthrough.spec.ts` which walks every route asserting no console errors, failed requests,
  or blank pages
- **Cross-cutting**: shared Go↔TS golden corpus (`internal/visual/testdata/corpus/` mirrored
  to `web/src/visual/__fixtures__/corpus/`, `make generate-corpus`); Makefile guards
  (`check-single-dist`, `check-raw-sql`, `check-docker`, `check-build-script`)

---

## 6. Recently completed (for context)

| Date | Work | Detail |
|---|---|---|
| 2026-08-19 | Browser walkthrough fixes | missing `dev/shepherd.dev.env` restored; audit API returned 0 rows unfiltered (SQL `''` vs NULL); canvas minimap unthemed and stacked over the zoom controls; dev Alloy pinned to v1.12.2 against a v1.18.1 schema |
| 2026-08-19 | Visual builder refinement | design tokens, draw.io-style dragging, working save/load, `make schema` — `docs/archive/visual-builder-refinement.md` |
| 2026-08-19 | Management API contract | protobuf/Connect with REST shims — `docs/archive/api-contract-design.md`; closed a pre-existing hole where `/api/admin/*` had no RBAC at all |
| 2026-08-18 | Audit fixes | 5 criticals, gitsync silent data loss (and a `pgtype.UUID` scan bug that had broken every git sync), 23 lint findings, lint + GoReleaser configs restored |
