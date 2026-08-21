# Completed work — 2026-08-19 baseline round

Extracted verbatim from `docs/project-status.md` on 2026-08-20. Every item here is **fixed or
shipped**; it is kept as the historical record of what the 2026-08-19 baseline round found and
closed. Not current instructions — the live ledger is `docs/project-status.md`.

The one exception is **F5** (S3 sandbox simulation), which was open at the time of this record and
has since been implemented but is **disabled by default with open containment criticals**. Its
current state is in the live ledger, not here.

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
hold; the feature is off by default and must stay off. See `docs/project-status.md` (the live ledger)
and `docs/reviews/s3-sandbox-security-findings.md`.

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

