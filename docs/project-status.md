# Shepherd — project ledger

> **Active work is tracked on the GitHub Project board, not here:**
> **https://github.com/users/procoduck/projects/1** ("Shepherd Implementation"). The board is the
> working tracker — status, assignment, and what's next. This document stays the human-readable
> **verified-baseline snapshot** (what demonstrably works, at which release/run), refreshed at each
> release; day-to-day item status moves to the board. The outstanding-work sequence that seeded the
> board is archived at `docs/archive/plans/2026-09-16-outstanding-work.md` — eight of its twelve
> remaining items shipped in v0.9.0/v0.10.0 and the other four are GitHub issues (§3).
>
> Baseline re-verified 2026-09-28 at the v0.11.0 release
> (`25145fc`, chart 0.15.0) from the CI and release runs on that commit, not from a summary.
> Completed rounds live in `docs/archive/` — the history this ledger used to carry inline is
> `docs/archive/completed-2026-09-11.md`. Do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — verified baseline, open bugs, unbuilt features, open follow-ups |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/plans/` | dated per-PR implementation plans while their work is unreleased; a plan moves to `docs/archive/plans/` once it has shipped in a tag. Current: `2026-09-28-receiver-tier.md` (#109, built, unreleased) and `2026-09-29-tenant-route-apply.md` (awaiting approval) |
| `docs/visual-builder-design-VB1.md` | visual builder design — M1–M8 built; §6.4 (S3) is the live spec for the sandbox feature (enabled by default in the Helm chart since v0.0.1) |
| `docs/reviews/` | **live decision records only**: `canvas-framework-evaluation.md` (the React Flow decision and the controlled-mode contract `CanvasPane` depends on). Closed reviews move to `docs/archive/reviews/` |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/git-provider-design.md` | GitOps provider-auth design; live at top level because Go source cites its § numbers |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/kind-test-environment-plan.md` | kind-based Kubernetes test environment (`make e2e-k8s`, weekly + path-filtered on qualifying PRs) plus §11 the reusable dev stack it shares pins with (`make dev-kind`, the Kubernetes flavour of `make dev`). Steps 1, 2, 4, 6 done; step 3 (full-values install, true previous-version upgrade spec) and step 5 (`NOTES.txt` CNI warning) still open — see its status header |
| `docs/gateway-tier-plan.md` | **in progress**: all 11 workstreams built (2026-08-22); W1, W2, W3, W5, W8 done; R1, R2 signed; R6's two conditions met in v0.9.0 (per-service-account rate limit, the `pipeline.propose` audit row), so W11 ships; R3 open with the receiver-tier build (#109) still to do; the product surfaces left are W7 onboarding (#111) and W9 chart-values + G10 (#112). Its §9 is the step ledger; §7 the review gates and sign-offs |
| `docs/proofs/` | red–green proofs for shipped controls. Not archived: Go source and CI workflows cite these paths |
| `docs/archive/` | finished work, kept as the record of why things are the way they are |

---

## 1. Verified baseline (2026-09-28, v0.11.0)

Every row is a CI or release run on `25145fc` (the v0.11.0 release commit, PR #163) or the run that
last exercised the surface, so the claim is checkable by run id rather than by trusting this table.

| Check | Where it ran | Result |
|---|---|---|
| Go build, vet, `govulncheck`, `go vet -tags e2ek8s ./e2e/k8s/` | CI `build` job, run 36400152608 (`25145fc`) | clean |
| `golangci-lint` + config verify, all ten `make guards`, `helm lint` (incl. the dev-kind values), `scripts/repocheck` | CI `lint` + `guards` jobs, same run | 0 issues |
| `go test ./...` with coverage (testcontainers Postgres) | CI `test` job, same run | green |
| `pnpm typecheck`, `biome check`, Vitest (unit + jsdom component) | CI `web` job, run 36397116495 (`1c05e52`, PR #162 — the last web change; the release commit only rebuilt the bundle) | clean |
| Mocked Playwright (`make test-ui`) | CI `test-ui` job, same run (36397116495) | green |
| `make smoke` + fullstack Playwright against the compose stack | CI `test-fullstack` job, run 36400152608 (`25145fc`) | green |
| Compose e2e, agent protocol incl. the `ssh` GitOps scenario (`make e2e`) | `e2e.yml`, manual dispatch on `25145fc`, run 36401527729 — dispatched because the path filter skipped it after #162 removed the REST shim | green (22 specs) |
| Kubernetes e2e, kind (`make e2e-k8s`) | `e2e-k8s.yml` on the release PR (#163, `24ebbf2`), run 36397765892 | green |
| Sandbox e2e (`make e2e-sim`) | `e2e.yml` `e2e-sim` job, same manual dispatch on `25145fc` (run 36401527729) | green (containment + run lifecycle) |
| CodeQL (actions, go, javascript-typescript, python; `security-extended`) | `codeql.yml` on `25145fc`, run 36400152824 | green |
| Release: verify job, goreleaser, image attestations, chart OCI push, `scan-published` | `release.yml`, run 36400183145 | success — chart 0.15.0 / appVersion 0.11.0 pullable, images `0.11.0` + `latest` present for both, SLSA v1 provenance verified (`gh attestation verify`, source `25145fc`), `scan-published` green for both |

### What demonstrably works end to end

Verified on the running stack and in the browser, not inferred:

- **Agent protocol** — real Alloy v1.19.2 agents register, poll and apply served config; status,
  hash and not-modified round-trip. Collector-token auth is a Connect request gate (v0.5.0): a
  bad credential is refused before the body is read.
- **Merge engine + validation gate** — served config carries both seeded pipelines, declare-wrapped,
  matchers resolving against real collector labels; role enforcement covers both the write-time
  and the live agent serve path (`internal/serve.ComputeServed`, one code path).
- **Management API** — `shepherd.mgmt.v1` Connect contract generated for Go and TypeScript; every
  legacy REST route preserved as a wire-compatible shim; fail-closed per-procedure authz with the
  org-editor tier; service-account auth as a request gate.
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
`docs/archive/completed-2026-09-11.md` with their red-run evidence.

---

## 3. Unbuilt / gated features

### F-CONTRIB — collector detail does not show contributing pipelines · **low**

Served config is shown, but nothing links back to the pipelines that produced it, so there is no way
to get from "this collector runs X" to "because pipeline Y matched". The merge engine already knows
the contributing set.

### Gateway-tier workstreams · see `docs/gateway-tier-plan.md` §7 "Sign-offs recorded 2026-09-11"

R1, R2 signed; R6's conditions met in v0.9.0; R3 open. Shipped since the sign-offs: tenant routes
UI (W4, v0.9.0), service accounts UI (W10, v0.9.0), the MCP interface in the release archives (W11,
v0.9.0), reconciliation (W6, v0.11.0). Still to build, each a GitHub issue:

- **Receiver tier (W4's other half) — #109.** Plan: `docs/plans/2026-09-28-receiver-tier.md`
  (config rendered at pod start, destinations from chart values, default off; tenant-route apply is
  a separate follow-up). Built: CLI (#169), chart (#170), kind proof (#171), docs + the R3 packet
  (gateway plan §7, 2026-09-29). **Awaiting the R3 sign-off**; tenant-route apply is the follow-up.
- **Onboarding artifacts page (W7) — #111.** "Connect an app" snippets for a tenant route.
- **Chart-values generator UI (W9) + gate G10 — #112.**

### Attribute-based matching — UI and docs · **#139 item 8** · done, unreleased

Built on `main`: org-editor toggles for both matching flags, matcher suggestions in the pipeline
editor and visual builder (fed by a `ListAttributes` that now lists only keys matching evaluates),
and the matchers docs section. Ships in the next release; closes #139.

Closed features (F5 sandbox simulation, F-SIGNAL-SERVE) are in `docs/archive/completed-2026-09-11.md`.
F-REVISIONS closed — see `CHANGELOG.md` v0.6.0 "Pipelines — Shipped"; its plan is archived at
`docs/archive/plans/2026-09-11-f-revisions.md`.

---

## 4. Follow-ups

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
- [ ] **Receiver tier build (R3)** — #109.
- [x] **R6 conditions** — the per-service-account rate limit shipped in v0.9.0 (#130); the
      `pipeline.propose` audit row already existed. `shepherd-mcp` joined the release archives in
      v0.9.0 (#132).
- [x] **Tenant routes UI** (W4) — shipped in v0.9.0.
- [x] **Service-accounts UI** (W10 remainder) — shipped in v0.9.0.
- [x] **Reconciliation surface** (W6) — shipped in v0.11.0 (#140).
- [ ] **Onboarding artifacts page** (W7) — #111.
- [ ] **Chart-values generator UI** (W9) + gate G10 in the kind suite — #112.

### Smaller follow-ups

B1–B3 come from the v0.6.0 manual UI walkthrough (`docs/archive/plans/2026-09-14-walkthrough-fixes.md`,
§3 "Blocked") — B2 and B3 have since shipped, B1 is still open — and two items from the kind dev stack's
first live bring-up (`docs/archive/plans/2026-09-14-kind-dev-stack.md`); every other finding from both
was closed in the same batches (`CHANGELOG.md` v0.7.0).

In rough priority order; closed items stay in place, marked with the release that shipped them:

- [x] **`ValidatePipeline` stays open to org readers** (decided 2026-09-28). Its Connect interceptor
      row is `auth.RoleOrgReader`: a reader can validate pipeline text, which writes nothing apart
      from the `pipeline.propose` audit row for service-account callers. The removed REST shim had put
      it behind org editor; that stricter rule is not carried over. `role_matrix_test.go` pins the
      Connect behaviour.
- [ ] **A collector's `APPLIED` status can mean "polled with the served hash", not "loaded it
      successfully."** `ClearStaleFailedStatus` promotes NULL/FAILED to `APPLIED` on a status-less
      poll carrying the served hash, and nothing Shepherd reads today (`effective_config` is
      unread; beacon rows are not keyed by collector instance) can tell a fresh load from a
      rejected one served from cache. Needs a reproduction against a live Alloy v1.19.2 agent
      before picking one of three options — see `docs/archive/plans/2026-09-14-walkthrough-fixes.md` §3
      (B1). GitHub issue #115.
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
      bump (Dependabot will propose it) once v1.85.0 stable is released. The other three modules in
      that group shipped via #153.
- [x] **`visual-drafts.spec.ts` flaked under load — it was a real bug.** The builder's draft
      autosave overwrote a pending draft on open (the schema-version stamp counted as an edit); the
      spec caught it whenever that save beat the restore check. Fixed on `main` (autosave now waits
      for the restore decision), with a deterministic regression spec.
- [x] **Bump Alloy for the 15 high CVEs in the bundled binary (done 2026-09-14, v1.19.2).**
      Trivy found 15 HIGH, unfixed-excluded CVEs in the v1.18.1 binary bundled into both images
      (built upstream with Go 1.26.5). v1.19.2 is built on a patched Go and carries 2, both in
      grpc (upstream). Bumped as a schema bump: `ALLOY_IMAGE`/`ALLOY_VERSION`, `make schema`,
      overlay reconciliation, the v1.18.1 artifact kept embedded for upgrade-review diffs.
- [ ] **Typed `Role`/`Source` enums.** `internal/auth`'s role constants (`RoleOrgAdmin` etc.,
      `internal/auth/authz.go`) and `pipelines.source` are plain `string`-typed constants, not a
      distinct Go type — the `exhaustive` linter cannot check a switch over either for
      completeness the way it can for the four enums W2-S5 closed.
- [ ] **Canvas keyboard wiring is partial.** Delete/undo/redo/copy-paste and re-focus-on-drop work
      (`web/src/visual/components/CanvasPane.tsx`); the a11y pass `docs/visual-builder-design-VB1.md`
      §8 calls for — keyboard node navigation, a focus ring on ports — is not built.
- [ ] **Nested `bindings[]` entries are unrenderable** — a known, accepted gap in
      `web/src/visual/bindings.ts` (W5-01/W5-02): a binding at a nested block's prop path writes
      correctly into `props` and renders, but the separate `GraphBinding`/`doc.bindings[]` channel
      stays flat-top-level-prop-only by design, not yet extended.
- [ ] **Kind suite, plan steps 3 and 5** (`docs/kind-test-environment-plan.md`): the full-values
      install and the true previous-version Helm upgrade spec (no longer blocked — charts 0.9.0
      through 0.14.0 are published), and the `NOTES.txt` CNI/NetworkPolicy warning (G10 is
      scheduled with the chart-values UI above).
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

Per-org agent tokens; webhook-triggered git sync; OpAMP; editing git-sourced pipelines in the UI; a
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
  specs over the Makefile, workflows, `versions.env` pins, `renovate.json`, the chart's
  `values.yaml`, `scripts/dev-kind.sh` and `dev/kind/*`, `.goreleaser.yaml`)
- **CI** (`ci.yml`, SHA-pinned actions): `changes` gates the expensive jobs on their inputs; `lint`,
  `build` (incl. `govulncheck` and the `e2ek8s`-tagged vet), `guards` (incl. `helm lint` and
  repocheck), `generated-drift`, `test` (`make test-cover`, coverage artifact), `web`, `test-ui`,
  `test-fullstack` (incl. `make smoke`). `e2e.yml` runs on push to main, path-filtered.
  `e2e-k8s.yml` weekly and on qualifying PRs. Scheduled: `schema-verify` and `govulncheck` weekly.
  `release.yml` on `v*` tags: verify job (incl. the Trivy image gate), goreleaser, provenance
  attestations, chart OCI push (refuses an appVersion/tag mismatch and an already-published
  chart version), then a report-only scan of the published images. `security-scan.yml` on every
  PR/push: gitleaks over the full history, Trivy over the two built images (gate on Shepherd's
  binary + base, report on the vendored Alloy binary) and Trivy misconfig over `deploy/`; weekly:
  the last released images and OpenSSF Scorecard. `main` requires the CI checks for everyone.
- **The repository is public, so standard-runner Actions minutes are not billed; GitHub still
  runs every job separately**, so a slow, noisy CI costs signal even when it costs no money.
  Three controls keep it in range — `paths-ignore` so a docs-only change never starts CI, the
  sub-minute `changes` job gating the two most expensive jobs on whether their own inputs moved,
  and weekly rather than nightly scheduling for the kind suite. **Reduce redundant executions,
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
