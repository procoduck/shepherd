# Shepherd — project ledger

> **The single live status document.** Baseline re-verified 2026-09-14 at the v0.6.0 release
> (`e4e45b8`, chart 0.10.2) from the CI and release runs on that commit, not from a summary.
> Completed rounds live in `docs/archive/` — the history this ledger used to carry inline is
> `docs/archive/completed-2026-09-11.md`. Do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — verified baseline, open bugs, unbuilt features, open follow-ups |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/plans/` | dated per-PR implementation plans; a plan moves to `docs/archive/plans/` once its work has shipped in a tag (`2026-09-14-walkthrough-fixes.md`, `2026-09-14-kind-dev-stack.md` are merged but unreleased) |
| `docs/visual-builder-design-VB1.md` | visual builder design — M1–M8 built; §6.4 (S3) is the live spec for the sandbox feature (enabled by default in the Helm chart since v0.0.1) |
| `docs/reviews/` | **live decision records only**: `canvas-framework-evaluation.md` (the React Flow decision and the controlled-mode contract `CanvasPane` depends on). Closed reviews move to `docs/archive/reviews/` |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/git-provider-design.md` | GitOps provider-auth design; live at top level because Go source cites its § numbers |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/kind-test-environment-plan.md` | kind-based Kubernetes test environment (`make e2e-k8s`, weekly + path-filtered on qualifying PRs) plus §11 the reusable dev stack it shares pins with (`make dev-kind`, the Kubernetes flavour of `make dev`). Steps 1, 2, 4, 6 done; step 3 (full-values install, true previous-version upgrade spec) and step 5 (`NOTES.txt` CNI warning) still open — see its status header |
| `docs/gateway-tier-plan.md` | **in progress**: all 11 workstreams built (2026-08-22); W1, W2, W3, W5, W8 done; R1, R2 signed and R6 signed conditionally on 2026-09-11, R3 open with the receiver-tier build scheduled; the product surfaces for W4/W6/W7/W9/W10 are §4 items here. Its §9 is the step ledger; §7 the review gates and sign-offs |
| `docs/proofs/` | red–green proofs for shipped controls. Not archived: Go source and CI workflows cite these paths |
| `docs/archive/` | finished work, kept as the record of why things are the way they are |

---

## 1. Verified baseline (2026-09-14, v0.6.0)

Every row is a CI or release run on `e4e45b8` (or the PR that produced it), so the claim is
checkable by run id rather than by trusting this table.

| Check | Where it ran | Result |
|---|---|---|
| Go build, vet, `govulncheck`, `go vet -tags e2ek8s ./e2e/k8s/` | CI `build` job, run 34829976236 (PR #65) | clean |
| `golangci-lint` + config verify, all ten `make guards`, `helm lint`, `scripts/repocheck` | CI `lint` + `guards` jobs, same run | 0 issues |
| `go test ./...` with coverage (testcontainers Postgres) | CI `test` job, same run | green |
| `pnpm typecheck`, `biome check`, Vitest (unit + jsdom component) | CI `web` job (PR #60, run 34826217159) | clean; **577/577** |
| Mocked Playwright (`make test-ui`) | CI `test-ui` job (PR #60, same run) | **259 tests in 46 files, green** |
| `make smoke` + fullstack Playwright against the compose stack | CI `test-fullstack` job, run 34829976236 | green |
| Compose e2e, agent protocol incl. the `ssh` GitOps scenario (`make e2e`) | `e2e.yml` on push to main, run 34829225428 (`7b82428`) | green |
| Kubernetes e2e, kind (`make e2e-k8s`) | `e2e-k8s.yml` on PR #65, run 34829976482 | green |
| Sandbox e2e (`make e2e-sim`) | `e2e.yml` `e2e-sim` job on PRs touching the sandbox surface (path-filtered, never on push), run 34828420060 (PR #62) | green |
| Release: verify job, goreleaser, image attestations, chart OCI push | `release.yml`, run 34831444506 | success — chart 0.10.2 / appVersion 0.6.0 published, images `0.6.0` present, provenance verifies. The report-only `scan-published` job failed on a wrong image name (fixed in the Unreleased changelog entry); the release itself was unaffected |

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

### History

The dated passes that used to sit here — the 2026-08-20 baseline, the 2026-08-21 W1 / sandbox
containment / health remediation passes, the 2026-08-22 gateway-tier build-out, the 2026-09-11
remediation with its D1–D14 decisions, and the v0.5.0 dependency and toolchain catch-up — are in
`docs/archive/completed-2026-09-11.md`, verbatim. `CHANGELOG.md` is the user-facing view.

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

R1, R2 signed; R6 signed conditionally; R3 open with the build scheduled. What is now scheduled
product work rather than a gate: tenant routes reaching users (W4, cleared by R1), the receiver
tier (W4's other half, to be built then brought back to R3), reconciliation (W6), onboarding
artifacts (W7), the chart-values UI + G10 (W9), teams UI (W10), and the two R6 conditions for the
MCP interface (W11). Each is a §4 item below.

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
- [ ] **Editor Format + Validate buttons**: `FormatPipeline` RPC over `alloy fmt`, wired to a
      Format button; an explicit Validate button beside the idle-debounced validation.
- [ ] **Experimental components as an org setting**: migration + proto field + server-side
      render gate (an experimental node with the toggle off is a render error), replacing the
      hardcoded client flag.
- [ ] **`shepherd_build_info` gauge** (labels `version`, `commit`) in `internal/metrics`.
- [ ] **REST shim deprecation**: changelog notice, `Deprecation` header on every shim route,
      removal scheduled one release later.
- [ ] **Receiver tier build (R3)**: chart Deployment + Service + NetworkPolicy (gateway the only
      ingress), tested off-switch, real-Alloy pass-through tenancy e2e; then R3 sign-off.
- [ ] **R6 conditions**: per-service-account request rate limit (server-side, keyed on the
      service-account id) — still open (nothing in `internal/mgmtapi/machine_auth.go`). The
      `pipeline.propose` audit row already exists: `ValidatePipeline` writes it for every
      service-account caller (`internal/mgmtapi/rpc_pipeline.go`, red-run in
      `attribution_test.go`, since 2026-08-22), and `propose_pipeline_revision` composes that RPC.
      MCP binary joins the release archives only after the rate limit lands.
- [ ] **Tenant routes UI** (W4, cleared by R1): create/list/rotate/revoke, with the
      identifier-not-authorizer caveat and edge-control guidance on the docs site.
- [ ] **Service-accounts UI** (W10 remainder): create/list/revoke with the role tier — no client
      in `web/src/api/transport.ts` and no page. Teams and explicit members shipped in v0.3.0
      (`web/src/pages/TeamsPage.tsx`).
- [ ] **Reconciliation surface** (W6): per-collector declared vs served vs observed drift.
- [ ] **Onboarding artifacts page** (W7): "connect an app" snippets for a tenant route.
- [ ] **Chart-values generator UI** (W9) + gate G10 in the kind suite.

### Smaller follow-ups

Three items below (marked with the plan link) come from the v0.6.0 manual UI walkthrough
(`docs/plans/2026-09-14-walkthrough-fixes.md`, §3 "Blocked") and two from the kind dev stack's
first live bring-up (`docs/plans/2026-09-14-kind-dev-stack.md`); every other finding from both
was closed in the same batches — see `CHANGELOG.md` Unreleased.

Open, in rough priority order:

- [ ] **A collector's `APPLIED` status can mean "polled with the served hash", not "loaded it
      successfully."** `ClearStaleFailedStatus` promotes NULL/FAILED to `APPLIED` on a status-less
      poll carrying the served hash, and nothing Shepherd reads today (`effective_config` is
      unread; beacon rows are not keyed by collector instance) can tell a fresh load from a
      rejected one served from cache. Needs a reproduction against a live Alloy v1.19.2 agent
      before picking one of three options — see `docs/plans/2026-09-14-walkthrough-fixes.md` §3
      (B1).
- [ ] **Wizards have no channel to say when they silently drop or add something** (`B2` in the
      same plan). `wizard.CommitResult`/`RenderWizardResponse` carry no `warnings` field, so the
      self-monitoring log-step default and the wizard-added-matcher badge (both shipped in the
      same batch) work around the gap client-side rather than closing it; a first-class warnings
      field is a `proto/` change, deferred.
- [ ] **`Chart.yaml` `kubeVersion` says `>=1.25.0-0`, but `cnpg.enabled` needs `>=1.29.0-0`**
      (`B3` in the same plan). Helm cannot express a conditional floor; raising the global one
      refuses plain installs that work today. The database docs state the operator-path floor;
      raising the chart floor is a chart minor — decide separately.
- [ ] **`NOTES.txt` prints `https://` for every `route.hostnames` entry** although a route may be
      plain http (the kind dev stack's is). Cosmetic; fix with the next chart release.
- [ ] **A 30 s sandbox run against a 30 s scrape interval captures one scrape or none**, depending
      on where the scrape jitter lands — the first `make dev-kind` run showed 0 series and the next
      two 21. Containment and capture are fine; the run window versus the pipeline's own interval
      is the product question (a minimum window, or a first-scrape trigger).
- [ ] **A chart version bump is pending**: `main` changes `templates/service.yaml`,
      `values.yaml` and `values.schema.json` (`service.appProtocol`, #69) under the published
      `0.10.2`; the next release must bump the chart, and the v0.6.0 "no template changed"
      preamble pattern does not apply.
- [ ] **Graph diff for visual pipelines.** The pipeline editor's revision diff is text-only
      (`RevisionDiff`, CodeMirror merge view); the visual builder page has no revision UI, so a
      visual pipeline's graph-level change is not diffable, only its rendered text. Restoring a
      visual pipeline from the text editor still restores the graph (`wizard_state` travels with
      the revision) for revisions written after migration 0019 — older rows carry no graph, so
      restoring one restores text only — only the *diff view* is text-only.
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
      install and the true previous-version Helm upgrade spec (no longer blocked — chart 0.9.0,
      0.10.0, 0.10.1 and 0.10.2 are all published), and the `NOTES.txt` CNI/NetworkPolicy warning (G10 is
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
