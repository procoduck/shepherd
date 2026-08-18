# Shepherd — Project Status & Progress Checks

> Last audited: **2026-08-18** · progress updated 2026-08-18 after lint/releaser restoration and the fix-audit-findings workflow (full codebase audit against `docs/spec.md` and
> `docs/visual-builder-design-VB1.md`). Update the checkboxes and the date as work lands.
> Companion ledger for the visual builder: `docs/vb1-progress.md`.

## Verified health at audit time

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `go test ./internal/...` — all packages green, including testcontainers-backed
      integration suites (agentapi, auth, mgmtapi, store) against real Postgres in Docker
- [ ] Web tests runnable — `web/node_modules` not installed on this machine (`pnpm install`)
      · last recorded state: Vitest 34/34, Playwright 87/87
- [x] `make lint` faithful to spec — `.golangci.yml` restored from spec section 20; 0 issues
- [x] `make release-snapshot` — `.goreleaser.yaml` restored from spec section 21; snapshot green end-to-end

## Spec milestones (docs/spec.md section 16)

- [x] M1 Skeleton — layout, cobra+viper, migrations (0001–0005), sqlc, Makefile, health endpoint
      *(caveat: root lint/release configs missing — see Tooling gaps)*
- [x] M2 Agent protocol — buf codegen, agentapi + token-auth interceptor, registration,
      instances, empty-config for unclaimed, lifecycle sweeper, integration suite
- [x] M3 Merge + validation — matcher engine, declare-wrap merge, hashing, serve cache with
      singleflight, 3-stage gate, `shepherd validate`
- [x] M4 Pipelines API — CRUD, revisions, enable gate, preview-matches, audit writes
- [x] M5 AuthN/Z — OIDC BFF, Graph transitive groups, sessions, RBAC middleware, admin APIs;
      plus local-admin login (LA-1, beyond spec)
- [ ] M6 Frontend core — 🟡 shell/login/collectors/pipeline editor work, but collector detail
      metadata is blank (R2-C3) and collectors list shows only cluster+role (R2-H2)
- [ ] M7 Destinations + Wizard — 🟡 destinations done both ends; wizard backend done
      (schema/commit), **wizard UI is an 8-line stub** (no stepper, no
      `/wizards/app-observability` route; Playwright test self-skips)
- [ ] M8 ADO GitOps — 🟡 ADO client, encrypted credentials, reconciler, Git UI page exist;
      **reconciler cannot update an existing git pipeline** (silent drop; see Criticals);
      missing endpoints: credential test, force sync, credential update
- [ ] M9 Hardening — 🟡 Helm chart complete (+values.schema.json, `make helm-lint`),
      `/metrics` done; Overview dashboard shows placeholder `—` stats, Audit UI is a stub
- [x] M10 Local E2E — compose stack (postgres, mock OIDC, mockmsft, shepherd, real Alloy),
      8 ordered scenario groups incl. APPLIED round-trip, gate, GitOps, RBAC, local admin

## Visual builder VB-1 (docs/visual-builder-design-VB1.md)

- [x] M1 Schema unification — alloy v1.18.1 artifact, 184 components, `GET /api/schema`
- [x] M2 Canvas core — React Flow, palette, node, inspector, L1 lint, store
- [x] M3 Codegen + gate — Go renderer, TS renderer, shared golden corpus, REST endpoints
- [x] M4 Read-only graph view + recreate-as-visual
- [x] M5 Simulation S1 (flow check) + S2 (relabel/log trace endpoints + UI)
- [x] M6 Upgrade machinery — upgrade-check, review UI, needs_upgrade filter, overlay migrations
- [ ] M7 Simulation S3 — sandbox run (simulator service, transform, capture harness, run UI,
      `/simulate/runs` endpoints) — **not started**; blocked on Criticals below
- [ ] M8 Hardening — incl. persisting `wizard_state` on pipeline PUT (deferred item)

## Critical findings — verified still present in code (fix before VB-1 M7)

From the 2026-08-18 adversarial reviews (20 findings: 5C/10H/5M — full text in
`docs/vb1-progress.md`). The five criticals, re-verified against source at audit time:

- [x] **R2-C1** Status stored as raw proto enum string (`RemoteConfigStatuses_APPLIED`) at
      `internal/agentapi/service.go:101`; UI color map keys on `APPLIED` → badges never light up
- [x] **R2-C2** `GetConfig` passes the wire UUID as instance *name*
      (`internal/agentapi/service.go:95`) → hostname overwritten on first poll
- [x] **R2-C3** `collectorResponse` (`internal/mgmtapi/orgs.go:88`) has no
      `alloy_version`/`os`/`last_seen`/instances; `GetCollector` omits even status →
      detail page metadata structurally blank
- [x] **R1-C1** Visual builder renders inside the padded shell instead of fullscreen
- [x] **R3-C1** Dev seed creates all pipelines with `matchers: []`
      (`internal/cli/dev.go:135`) → merge engine matches nothing; dev stack serves empty
      configs while looking healthy
- [ ] HIGH findings — R2-H1, R2-H2, R1-H3, R3-H1..H4 fixed by the 2026-08-18 workflow; **R1-H1, R1-H2, R3-H5 remain** — see `docs/vb1-progress.md`
- [ ] MEDIUM findings (5) — see `docs/vb1-progress.md`

## Functional gaps in "done" features

- [x] **GitOps update path**: `internal/gitsync/reconciler.go:182` — updating an existing
      git-sourced pipeline is a bare `return nil` TODO. After first sync, changes to a
      repo's `.alloy` files are **silently dropped**. E2E only covers initial sync, so CI
      stays green. *Highest-priority silent-data-loss bug.*
- [ ] `POST /api/orgs/{org}/ado-credentials/{id}/test` (spec section 12) — not implemented
- [ ] `POST /api/orgs/{org}/repo-links/{id}/sync` (force sync, spec section 12) — not implemented
- [ ] `PUT`/`PATCH` on ado-credentials — not implemented
- [ ] Wizard `render` preview endpoint (spec section 12) — only `commit` exists
- [ ] Wizard UI stepper + `/wizards/app-observability` route (`web/src/pages/WizardsPage.tsx`
      is an 8-line stub; `web/tests/specs/wizard.spec.ts` self-skips when absent)
- [ ] Audit page UI (`web/src/pages/AuditPage.tsx` is a stub; backend `GET /audit` works)
- [ ] Overview dashboard real stats (collectors/pipelines/clusters counts are `—`)
- [x] Collectors list columns: status (colored), last-seen, version (R2-H2)
- [ ] Health endpoint DB-connectivity + pending-migration check
      (`internal/server/server.go:243` TODO)
- [ ] Stale comment cleanup: `internal/server/server.go:170` "TODO milestone 6: serve
      embedded SPA" — SPA serving is actually implemented on the next line

## Tooling / repo gaps (likely lost in the machine-to-machine copy — root dotfiles dropped)

- [x] Restore `.golangci.yml` — exact config is printed verbatim in spec section 20; without it
      `make lint` runs golangci-lint defaults
- [x] Restore `.goreleaser.yaml` — exact config in spec section 21; `make release-snapshot` broken
- [ ] CI workflows (`.github/`) — none exist
- [ ] Dependency: `github.com/lib/pq` v1.12.3 (indirect via golang-migrate's
      `database/postgres` driver ← `internal/store`) carries 7 govulncheck findings
      (malicious-server panics/OOM) + deprecated `x/crypto/openpgp`. Fix: switch migrate to
      its pgx driver, or bump when patched. Low practical risk (own DB endpoint) but reachable.
- [ ] `docs/vb1-progress.md` cites commit SHAs from the pre-reset history (0f0e1f8 …)
      that no longer resolve in this repo — annotate or accept as historical

## Suggested order of attack

1. Restore `.golangci.yml` / `.goreleaser.yaml` from spec section 20–21 (mechanical)
2. Fix the five verified CRITICALs (R2-C1 → R2-C2 → R2-C3 → R1-C1 → R3-C1)
3. Fix the gitsync update path (silent data loss)
4. HIGH findings, then VB-1 M7 (S3 sandbox) and M8 hardening
5. Wizard UI stepper, Audit UI, Overview stats, missing REST endpoints

## Test infrastructure reference

- **Backend**: Ginkgo v2 + Gomega (15/24 packages have suites); testcontainers Postgres for
  integration; `make test` / `make test-integration` / `make e2e` (compose: postgres,
  mock-oauth2 OIDC, mockmsft Graph+ADO mock with `/__fixture` injection, shepherd, real Alloy)
- **Frontend**: Vitest units (API client, editor schema, visual L1/store/renderTS/draft);
  27 mocked Playwright specs (`web/tests/specs/`); 7 fullstack Playwright specs against the
  Docker dev stack (`web/tests/fullstack/`, `make test-fullstack`)
- **Cross-cutting**: shared Go↔TS golden corpus (`internal/visual/testdata/corpus/` mirrored
  in `web/src/visual/__fixtures__/corpus/`, `make generate-corpus`); 17 red-green proofs in
  `docs/proofs/`; Makefile guards (check-single-dist, check-raw-sql, check-docker,
  check-build-script)
