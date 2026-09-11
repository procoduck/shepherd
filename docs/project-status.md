# Shepherd — project ledger

> **The single live status document.** Baseline re-verified 2026-09-11 by the wave orchestrator
> running everything in §1 below, not by reading a summary. Completed rounds live in
> `docs/archive/` — do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — verified baseline, open bugs, unbuilt features |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/visual-builder-design-VB1.md` | visual builder design — M1–M8 built; §6.4 (S3) is the live spec for the sandbox feature (enabled by default in the Helm chart since v0.0.1 — see F5) |
| `docs/reviews/` | **live findings and decision records only**: `canvas-framework-evaluation.md` (the React Flow decision) and this session's own `2026-09-09-remediation.md`; closed reviews move to `docs/archive/reviews/` |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/kind-test-environment-plan.md` | kind-based Kubernetes test environment (`make e2e-k8s`, weekly + path-filtered on qualifying PRs): chart deploy, Gateway API route conformance + live attachment verification, §5 Layer B NetworkPolicy/simulator containment (all seven probes + the kill probe), install/upgrade repeatability. See the plan's own status header for the current step/feature count |
| `docs/gateway-tier-plan.md` | **in progress** (all 11 workstreams built as of 2026-08-22; W1–W3 done, the rest awaiting review gates R1/R2/R3/R6 — R5 was resolved 2026-08-22 — see its §9): multi-session plan for the tenant-aware gateway tier, the beacon and outcome verification, signal/role enforcement, the chart-values generator, teams/scoped identity and the agent (MCP) interface — 11 workstreams with its own step ledger (§9), 15 conformance gates (§6), review gates (§7) and the actor model (§3a) |
| `docs/proofs/` | red–green proofs for current work |
| `docs/archive/` | finished work, kept as the record of why things are the way they are |

---

## 1. Verified baseline (2026-09-11)

Re-baselined at the head of the 2026-09 remediation branch (`remediation/2026-09`, base
`39f724f`). Every row below is the wave orchestrator's own run, not a docker-running local repeat
of it (this docs pass does not run Docker) — command and date are on every row so the claim is
checkable against a fresh run at any time.

| Check | Command | Result (2026-09-11) |
|---|---|---|
| Go build | `go build ./...` | clean |
| Go vet | `go vet ./...` | clean |
| golangci-lint | `golangci-lint run ./...` | **0 issues** |
| `make lint` | `make lint` | 0 issues — all ten `guards` targets plus `golangci-lint` config verify |
| repocheck guards suite | `go test ./scripts/repocheck/ -count=1` | green |
| Go test suite | `go test ./...` | **41 packages ok.** One rerun in progress: `internal/agentapi`'s testcontainers Postgres died when `make smoke` built images concurrently on the same run — a resource contention artifact of the orchestrator's parallel verification, not a code defect; tracked, not yet re-confirmed green as its own line |
| Frontend typecheck | `cd web && pnpm typecheck` | clean |
| Frontend lint | `pnpm lint` (Biome check — the read-only pass CI runs) | clean |
| Vitest (unit + jsdom component) | `cd web && pnpm test` (`vitest run`) | **564/564** |
| Mocked Playwright | `make test-ui` | **254/254** |
| Container smoke | `make smoke` | **PASSED** |
| Compose e2e (agent protocol) | `make e2e` | gate running at baseline time — not yet reported |
| Sandbox e2e (containment + run lifecycle) | `make e2e-sim` | pending |
| Kubernetes e2e (kind) | `make e2e-k8s` | pending |

The three e2e rows are marked pending/running rather than assumed green: this docs pass does not
run Docker (ground rule for this session), and the orchestrator's own e2e passes had not reported
back as of 2026-09-11. Update them from the orchestrator's next report rather than inferring a
result from the rest of the table being green.

### Superseded — 2026-08-20 baseline (kept for history, not current state)

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` | clean |
| `golangci-lint run ./...` | **0 issues** |
| `go test ./...` | **all 20 packages green** (testcontainers suites included) |
| _(as of 2026-08-22: 37 packages green — the figures in this table are the 2026-08-20 baseline snapshot and are not re-measured on every change; see the dated sections below for current state)_ | |
| `cd web && npx tsc --noEmit` | clean |
| `npx vitest run` | **272/272** across 15 files |
| `npx playwright test` (mocked) | **148/148, zero skips** |
| `make lint` (incl. all guards) | 0 issues; `check-single-dist`, `check-dist-consistency`, `check-build-script`, `check-raw-sql`, `check-docker` all OK |

### What demonstrably works end to end

Verified on the running stack and in the browser, not inferred:

- **Agent protocol** — real Alloy v1.18.1 agents register, poll and apply served config; status,
  hash and not-modified round-trip
- **Merge engine + validation gate** — served config carries both seeded pipelines, declare-wrapped,
  matchers resolving against real collector labels
- **Management API** — `shepherd.mgmt.v1` Connect contract generated for Go and TypeScript; every
  legacy REST route preserved as a wire-compatible shim; fail-closed per-procedure authz
- **All 12 SPA routes** as of this 2026-08-20 baseline — walked in Chrome with a console/network collector: zero console errors,
  zero JS exceptions, zero failed requests. (The route count has since grown to 21 —
  `/teams`, `/admin/users`, `/admin/auth`, the three visual-builder canvas routes — per
  `web/src/routes/router.tsx`; those additions are covered by `route-guard.spec.ts` and the
  mocked suite, not by a repeat of this specific browser walk.)
- **Visual builder** — schema-driven palette (184 components, 314 named ports, 0 unnamed),
  draw.io-style connection dragging, delete/undo/redo, minimap, save/load with matchers
- **GitOps** — Gitea credential + repo link syncing, status `ok` (F9 shipped)
- **Wizard, audit, overview, admin CRUD, org switcher** — all functional
- **S3 sandbox run** — a live run completes in ~20s with 21 captured series and 3/3 healthy
  components. **Enabled by default in the Helm chart since v0.0.1** (both containment gates
  closed 2026-08-21) — see F5 below; the compose stacks keep their own opt-in `sim` profile.

### 2026-08-21 — signal derivation + role enforcement (W1)

`docs/gateway-tier-plan.md` W1. New `internal/signals` package derives a pipeline's signal set
(metrics/logs/traces/profiles) from its Alloy syntax and the schema artifact's wire types, and
holds the role → allowed-signals policy table; `internal/merge.WithRoleEnforcement` excludes a
role-mismatched pipeline from an assembled config instead of shipping it silently, recorded in
both `AssembleResult.Exclusions` and the generated header comment; `ValidatePipeline` surfaces a
pipeline's derived signals at authoring time. Full step ledger and the demonstrated red run are in `docs/gateway-tier-plan.md`
§9 and §10 — not duplicated here. The gap this pass originally left open (enforcement not reaching
`internal/agentapi`'s live `GetConfig` recompute path) was closed on 2026-08-22; see F-SIGNAL-SERVE
below.

### 2026-08-22 — the gateway-tier plan's remaining ten workstreams

All eleven workstreams of `docs/gateway-tier-plan.md` are now built; its §9 ledger is the detailed
record and is not duplicated here. In short: the Gateway API foundation and tenant-route renderer
with in-cluster attachment verification (W3/W4), destination templates with credential-free tenant
bindings (W2, merged separately as PR #11), the beacon and its baseline pipeline plus Grafana
outcome verification (W5/D7), three-way reconciliation (W6), onboarding artifacts (W7), five
catalog wizards (W8), the k8s-monitoring chart-values generator (W9), teams with scoped write and
capability-scoped machine actors (W10), and a read-plus-propose MCP interface (W11).

**Nothing here is user-reachable yet**, with one exception: an application administrator now sets
an org's **tenant identity** at creation (D11, `0013_org_tenant_id`), and the org admin screen
shows it. Everything else — W6, W7, W9, D7, W10's team/service-account services, W11 — has no UI,
and several have no RPC surface either; they are libraries with tests. The receiver tier is not
deployed and must not default on before R3.

**Review gates outstanding: R1, R2, R3, R6** (R5 was resolved 2026-08-22 — a machine may not issue credentials; R6 is partly resolved, with agent proposals now audited). A workstream is not done when its tests pass; it
is done when its gate is signed. Three defects found by the final review are fixed but worth a human reading: tenant identity
was caller-supplied, so an org admin could mint a route injecting another org's tenant (now an
app-admin-set org property, D11); D10's pass-through tenancy did not preserve the tenant header through the batch processor
(every tenant would have shipped untagged), and a machine actor's on-behalf-of claim was recorded
unverified before being checked against the credential's delegating human.

### 2026-08-21 — sandbox-containment pass

Closed F5's two enablement gates and B-CONCAT in one session (branch `feat/sandbox-containment`).
Details live in each item's own section; the short form:

- **B-CONTAIN-1 fixed** — bind-address hardening in both compose files (shepherd stays a
  `sim-internal` member but listens only on its pinned `default`-network IP; ports published
  IPv4-loopback-only after the dual-stack publish trap surfaced), red/green-proven at both the
  compose-declaration level and by live `P-shepherd-deny`/`P-shepherd-deny-api` probes.
- **Layer B built and green** — `e2e/k8s/simulator_containment_test.go`, all seven probes + the
  kill probe. Two harness defects found and fixed on the way (a Felix-convergence race and
  `kubectl debug --attach` swallowing exit codes — see the kind plan's §8b); the debugging also
  hand-verified the chart's policy denies pod-IP/ClusterIP/FQDN paths and that the CNI enforces
  egress, which the ingress-only negative control had never established.
- **B-CONCAT fixed** — `array.concat`-of-pure-references carve-out in `CheckEndpoints`, plus the
  renderer↔guard cross-test that was missing.
- Both gates being closed is what made the F5 enablement decision possible: the Helm chart now
  ships `simulator.enabled: true` (product decision, same day — see F5). Compose keeps its own
  opt-in `sim` profile regardless, by design, not because a gate is still open.

### 2026-08-21 — health-remediation pass

One session, four parallel workstreams, from a verified full-repo review. Summary only — the
diff is the detail.

- **CI can actually build images.** `docker-build-local` no longer hardcodes
  `--platform linux/arm64` (which failed with `exec format error` on CI's amd64 runners, so the
  image-building e2e targets had never run in CI); the duplicate `docker-build`/`deploy/Dockerfile`
  pair collapsed into one native-platform `docker-build-local` + `deploy/Dockerfile.local`, with
  `docker-build` kept as a deprecated alias.
- **Makefile honesty + ergonomics.** `make test`'s comment now says what it does (all Go tests,
  Docker required); the fictitious `test-integration` deleted (`-tags=integration` matched zero
  files); phantom `migrate` dropped from `.PHONY`; new `help` (default goal), `clean`,
  `clean-docker` and `tools` targets; the guards gained a real `page.route` grep gate over
  `web/tests/fullstack/` (`check-no-route-mocks`).
- **Controls that were only claimed now run.** `make helm-lint` joined CI's guards job; a
  `generated-drift` CI job fails on stale `make generate` output; `make schema-verify` runs on a
  weekly scheduled workflow; `make e2e-k8s` runs nightly.
- **Cannot-fail tests removed or rewritten.** The specs that asserted on fixtures they built
  themselves or could not fail (server-router/metrics specs, the PKCE spec that split its own
  string literal, tautological "red run" specs, the zero-assertion debug spec, always-true
  header/diagnostics assertions) are gone or re-pointed at production code; reader-persona RBAC
  negatives added to the mocked suite.
- **Dead code sweep.** Unused test helpers and dead fixtures removed.
- **Docs truth pass.** Claimed-but-nonexistent CI gates struck or made real; the dev password is
  documented as `admin` / `admin` everywhere; `e2e/AGENTS.md` written; the S3 security findings
  file carries per-finding statuses (B-CONTAIN-1/2 above remain the live criticals); root
  AGENTS.md, README, dev-guide seed table and the stale proof scopes refreshed.

What the first live CI runs then caught (same day, same branch — each was invisible while the
suites never ran):

- **Visual save was broken for any attribute-edited pipeline.** InspectorPanel's `setProp` passed
  `block_order: undefined` for plain attributes, `updateNode`'s spread planted the key, and the
  save path's protobuf Struct conversion threw `google.protobuf.Value must have a value` before
  any request — surfacing only as a transient toast. `updateNode` now drops explicitly-undefined
  patch values (red-run-proven store test), the save boundary JSON-round-trips `wizard_state`,
  and the newly reachable spec tail exposed a wrong assertion (it demanded the single-wire
  list-of-lists `targets = [...]` form Alloy refuses — now pinned to the bare-reference contract
  render.go documents).
- **The schema artifact was darwin-flavored.** The extractor executes Alloy's `SetToDefault` via
  reflection, so the artifact is GOOS-dependent; the committed one (generated on a mac) was
  missing 28 linux platform defaults the linux fleet actually gets. schema-verify's first
  completed CI run caught it. run.sh now always runs the extractor in a linux container
  (GO_IMAGE pin, module cache mounted, docker preflight), the linux artifact is committed, and
  overlay reconciliation reported zero disposition changes.
- **Runner-resource + caching fixes.** e2e-k8s and schema-verify free unused runner toolchains
  (first runs died on ENOSPC / an OOM-killed step); Docker layer, Playwright browser, alloy
  checkout, and a schema-verify-scoped Go module cache added — schema-verify's cold run is
  ~26 min (full alloy compile), warm runs restore the build cache.
- **`make e2e` ginkgo scope.** `./e2e/...` recursed into the tag-excluded `e2e/k8s` package and
  had broken `make e2e` for everyone since the kind suite landed; now `./e2e`.

All four workflows (CI incl. test-fullstack, E2E, E2E K8s, Schema Verify) are green on this
branch as of 2026-08-21 — each earned its first-ever green during this pass.

### 2026-09-11 — 2026-09 remediation

Opened from the 2026-09-09 review (`docs/reviews/2026-09-09-remediation.md`), seven parallel
workstreams (W1-W7) plus this docs pass (W8), on `remediation/2026-09` off `39f724f`. Full
per-commit detail is `git log --oneline 95f82a0..HEAD`; this is the summary.

**Settled decisions D1-D14** (recorded in full in the wave-0 commit, `git show 0d0669d`):
D1 merge PR #5 (grpc 1.83.1) + the x/crypto v0.56.0 bump (closes GO-2026-6354/6355, reachable
through `internal/gitrepo`'s SSH transport); D2 `@testing-library/react` + `jsdom` as frontend
devDependencies, enabling jsdom component tests; D3 service accounts get a configurable role
tier (editor/admin), default editor, existing rows backfilled editor; D4 local sign-in throttled
via `x/time/rate`, per login and per source IP; D5 the REST shim's pipeline-write/validate/
wizard/visual groups require org-editor; D6 simulate is org-editor everywhere (Connect, REST,
proto comments); D7 OIDC sessions additionally end at `id_token_expires`; D8 light mode
(`prefers-color-scheme` default, toggle stores an override); D9 simulator bearer token sourced
from `existingSecret`, then External Secrets, then a chart-generated Secret; D10 sandbox
NetworkPolicy drops cluster DNS egress, harness endpoints dialled at loopback; D11 `make e2e`
runs on push to main, path-filtered, since `merge_group` never fires without a configured merge
queue; D12 provenance attestation on release archives/images; D13 the sandbox fullstack spec
stays local-only (Docker-gated) rather than joining CI; D14 app `v0.4.0`, chart `0.10.0`.

**By workstream:** W1 rebuilt the CI/build surface around ten Makefile `guards`, `govulncheck`,
a `release.yml` verify job, and every workflow Action SHA-pinned. W2 made gitsync fail closed
with a Stage-3 merge dry-run on every synced file, extracted `internal/serve.ComputeServed` so
both agentapi and mgmtapi recompute paths share one code path (hash-identical, proven), and
converted three more raw-SQL sites to sqlc. W3 added the org-editor tier throughout (service
accounts, REST shim, proto), throttled local login, bound OIDC sessions to the ID token's own
expiry, and gave local team members the same reader floor OIDC team members already had — see
§7.2/§7.3a of `docs/spec.md`. W4 hardened the sandbox (minimal child-process env, rune-safe
stderr truncation, the chart's three-source token precedence, loopback-only harness egress) and
proved NetworkPolicy enforcement in a real cluster. W5 shipped nested secret bindings via a
picker, scalar fan-in with an Undo toast, `edge.order`-stamped reordering, IndexedDB draft
autosave, and moved the visual corpus to be read directly from `internal/visual/testdata`. W6
built out `components/ui` (Modal, DataTable, Field, …), light mode, route-level `requiredRole`
guards enforced client-side (server remains authoritative), and split three 500+-line admin
pages. W7 added a jsdom component-test harness, seeded deterministic local editor/viewer users
in the dev stack, a persona-floor guard so appAdmin cannot dominate the mocked suite by default,
a `waitForTimeout` budget guard, and fixed the SSH known-hosts `$HOME` dependency that had been
failing the compose GitOps scenario (F9-a, below).

**Orchestrator-verified results:** §1's 2026-09-11 table above. **Open follow-ups, not yet
built:**

- **Typed `Role`/`Source` enums.** `internal/auth`'s role constants (`RoleOrgAdmin` etc.,
  `internal/auth/authz.go`) and `pipelines.source` are plain `string`-typed constants, not a
  distinct Go type — the `exhaustive` linter (§20) cannot check a switch over either for
  completeness the way it now can for the four enums W2-S5 closed.
- **Remaining `mgmtapi` `mapError` sites.** `mapError` now backs `rpc_pipeline.go`,
  `rpc_admin.go`, `rpc_destination.go`, `toConnectError`, and `oidcSettingsError` (W2-S7/S7b/S7c).
  At least one hand-rolled `pgx.ErrNoRows` → `CodeNotFound`/`CodeInternal` mapping remains outside
  that set: `rpc_user.go`'s `UpdateUser`.
- **Retiring the REST shim.** `docs/gateway-tier-plan.md` W10's review already found the REST
  shim "cannot carry a service-account identity at all" (recorded stricter-not-looser in
  `router.go`) — a real capability gap, not yet a scheduled removal. No decision has been made to
  retire it; callers still depend on it.
- **`gochecknoglobals`.** W2-S10 removed 18 stale `nolint:gochecknoglobals` directives — the
  linter itself was never enabled (not present in `.golangci.yml`'s §20 list), so the directives
  suppressed nothing. Undecided: enable the linter for real, or drop the vocabulary.
- **Nested `bindings[]` entries are unrenderable** — a known, accepted gap in
  `web/src/visual/bindings.ts` (W5-01/W5-02): a binding at a nested block's prop path (e.g.
  `endpoint[0].basic_auth.password`) writes correctly into `props` and renders, but pushing that
  same path into the separate `GraphBinding`/`doc.bindings[]` channel would emit invalid Alloy —
  that channel stays flat-top-level-prop-only by design, not yet extended.
- **Canvas keyboard wiring is partial.** Delete/undo/redo/copy-paste and re-focus-on-drop work
  (`web/src/visual/components/CanvasPane.tsx`); the a11y pass `docs/visual-builder-design-VB1.md`
  §8 calls for — keyboard node navigation, a focus ring on ports — is not built.
- **Fullstack Playwright specs pending wave 3.** `web/tests/fullstack/` covers auth, admin CRUD,
  org data, pipelines, protected routes, the walkthrough, wizard commit, and two visual round-trip
  specs (10 files). Still not written: roles, rollout, wizard-commit-depth, matcher-edit, and
  sandbox-run fullstack coverage — needs the dev stack running, out of scope for a docs-only pass.

---

## 2. Open bugs

### B-CONTAIN-1 — the sandbox can reach the control plane · **critical** (S3 only) · [FIXED 2026-08-21]

`shepherd` is attached to `sim-internal` in **both** compose files, alongside `simulator`.
`internal: true` denies egress to the internet; it does nothing about *neighbours*. The sandbox
scraped Shepherd's unauthenticated metrics port and the data was returned to the user in run
results.

**Fix (bind-address hardening — Option C of `docs/archive/reviews/b-contain-1-bind-hardening.md`).** Both compose files now pin
`ipam.config.subnet` on `default`/`sim-internal` and give `shepherd` a static `ipv4_address` on
each (e2e `172.28.0.10` / `172.28.1.10`, dev `172.29.0.10` / `172.29.1.10` — distinct ranges so the
two stacks can run concurrently). `SHEPHERD_SERVER_LISTEN` / `SHEPHERD_SERVER_METRICS_LISTEN` are
set to shepherd's own `default`-network literal instead of the bare `:8080`/`:9090` that bound
every interface, including `sim-internal`; the healthcheck's `--addr` moved off `localhost` to the
same literal. **Zero Go changes** — `Server.Listen`/`MetricsListen` were already plain
`http.Server.Addr` strings (`internal/server/server.go:111-123`,
`internal/config/config.go:225-268`).

`internal/simsvc/compose_containment_test.go` now pins the declaration and is genuinely red/green:
`shepherd.Networks` was reshaped to decode compose's long map form (`ipv4_address` per network) as
well as the short list form; new assertions require `SHEPHERD_SERVER_LISTEN`/`_METRICS_LISTEN` to
be set, non-bare, non-`0.0.0.0`, and prefixed with shepherd's own pinned `default`-network address
(which must differ from its `sim-internal` address). `e2e/sandbox_egress_test.go` gained three
probes reading shepherd's real addresses from `docker inspect` (not hardcoded): `P-shepherd-control`
(shepherd reachable on its own `default` address, dialled at `/metrics`), `P-shepherd-deny` /
`P-shepherd-deny-api` (shepherd's `:9090`/`:8080` NOT reachable from the sandbox's network
namespace, dialled at shepherd's `sim-internal` address). Both denial probes target real
unauthenticated paths (`/metrics`, `/healthz`) rather than bare `/`: shepherd's metrics mux only
serves `/metrics` and the main API 404s every unmatched path, so a bare-`/` probe would 404
whether or not the network actually blocked it — a reachable-but-404 response and a connection
refusal both make `wget` exit non-zero, which would have made the denial probes pass even with
containment removed (the exact "test that can't fail when the control is removed" trap this
repo's own testing standard forbids). Caught and fixed before closing this entry.

**IPv6 publish-port side effect, also fixed.** Pinning shepherd's listener to a literal IPv4
address made the container-side socket IPv4-only (it was previously dual-stack — Go's
`net.Listen("tcp", ":8080")` binds `[::]:8080` and accepts both families on Linux). Docker/OrbStack
still published shepherd's host ports dual-stack (`0.0.0.0:PORT` **and** `[::]:PORT`); on a
dual-stack host `localhost` resolves to `::1` first, so `curl -sf localhost:8080/healthz` (and the
e2e suite's own `SynchronizedBeforeSuite` healthz wait) connected over the IPv6-published half,
found nothing listening on any IPv6 address in the container's netns, and got RST (`Connection
reset by peer`, curl exit 56) — first caught as `make e2e-egress` failing at `Ran 0 of 38 Specs`
with the `BeforeSuite` timing out, confirmed precisely with `curl -4 localhost:18080/healthz`
succeeding while bare `curl localhost:18080/healthz` reset. Fix: publish shepherd's host ports
IPv4-only (`127.0.0.1:18080:8080` / `127.0.0.1:18090:9090` in the e2e file,
`127.0.0.1:8080:8080` in dev) instead of the default `0.0.0.0`/dual-stack publish — removes the
broken IPv6 half outright rather than relying on client-side IPv4 fallback. Shepherd's
container-side bind stays the pinned IPv4 literal; the containment control itself is unchanged.
No other service needed this — every other compose service still binds its in-container listener
to a bare port or `0.0.0.0`, so their dual-stack publish has a real listener behind both halves.

**Verified 2026-08-21:**
- `go test ./internal/simsvc/ -count=1` — 85/85 green. Red-proved: reverting the e2e file's two
  `SHEPHERD_SERVER_*_LISTEN` env vars to `:8080`/`:9090` fails the new assertion exactly
  ("a bare :port binds every interface, including sim-internal"); restored, green again.
- `make e2e-egress` — **green, 11/11 specs** (`P-control`, `P-deny-name`, `P-deny-ip`, `P-topology`,
  `P-shepherd-control`, `P-shepherd-deny`, `P-shepherd-deny-api`, plus the literal-retarget and
  runtime-retarget scenarios), full teardown clean.
- e2e-level red-run, run twice to isolate each denial probe (Ordered spec containers stop at the
  first in-container failure, so both vars reverted together only reds the first probe):
  reverting both `SHEPHERD_SERVER_LISTEN`/`_METRICS_LISTEN` to `:8080`/`:9090` reds
  `P-shepherd-deny` ("the sandbox reached shepherd's metrics endpoint at 172.28.1.10 —
  B-CONTAIN-1 is back"); reverting only `SHEPHERD_SERVER_LISTEN` (metrics pinned correctly) reds
  `P-shepherd-deny-api` on its own ("probe output: ok / Expected an error to have occurred. Got:
  nil") — the sandbox reached shepherd's `/healthz` in both cases when the corresponding bind was
  reverted. Both vars restored, full suite re-confirmed green (11/11).
- Dev-stack smoke (decision record §5 step 4): dev compose up, `curl -sf localhost:8080/healthz` (no `-4`)
  succeeds — `ok`, exit 0 — confirming the IPv6 publish fix works on the dev stack too; torn down
  clean.

### B-CONTAIN-2 — `internal: true` does not deny the Docker host · **local dev only**

The bridge gateway is in-subnet, so on OrbStack every host-published port on the machine is
reachable from the sandbox — proven end to end. Docker Engine blocks it, so CI cannot catch it
either way.

**Scope corrected 2026-08-20:** this is an artifact of Docker bridge networking and does not exist
in Kubernetes, which is the production target. The Helm chart already ships a default-deny
NetworkPolicy on both Ingress and Egress (plus `automountServiceAccountToken: false`, non-root,
read-only rootfs, dropped capabilities), and the sandboxed Alloy is a child process of the
simulator pod, so the pod's network boundary is the sandbox boundary. Compose stays a
local-development convenience and this stays documented rather than fixed.

The real residual risk is different and is now the thing to close: **a NetworkPolicy is only
enforced if the CNI implements it** — Flannel silently ignores it — and nothing has ever verified
the policy's effect in a real cluster. Plan: `docs/kind-test-environment-plan.md`.

### B-CONCAT — `CheckEndpoints` refuses the expression the renderer now emits · [FIXED 2026-08-21]

The M13 fix made `render.go` emit `array.concat(...)` when several discovery sources fan into one
scrape; `simsvc.CheckEndpoints` refused every call expression, so any fan-in graph could not run in
the sandbox. The guard now treats exactly `array.concat(...)` over pure component references as
transparent — any other callee, and any argument that is a literal, nested call, or expression,
still refuses fail-closed. Red-proved: disabling the carve-out fails exactly two named specs
(guard_test.go's fan-in acceptance and crossguard_test.go's renderer round-trip) while the three
refusal-side specs stay green. The missing renderer↔guard coupling now exists:
`internal/simsvc/crossguard_test.go` renders the committed fan-in corpus fixture through the real
`visual.Render` and asserts `CheckEndpoints` accepts the actual output, so the next renderer change
that emits a new expression form fails there, loudly, instead of at a user's sandbox run.

### B-STAGEORDER — `loki.process` stage order is not preserved · [FIXED 2026-08-20]

The order was not merely lost at render time, it was never stored: `Props` is a map keyed by block
name. `GraphNode.block_order` (proto field 8) now records the authored sequence, and both renderers
re-sequence the component's blocks before writing the body. The parser records the order it reads,
so a round trip no longer re-sequences an existing config, and the inspector maintains it as blocks
are added and removed.

Empty means "no recorded order" and falls back to schema order, so graphs saved before the field
render byte-identically. Red-proved in both languages, confirmed to load in a real `alloy run`, and
checked through the live API in all three cases (both authored orders and the fallback).

### F9-a — `ssh` auth kind fails in the compose stack · **FIXED 2026-09-10, pending the e2e gate**

Root cause (confirmed by reading go-git v6's ssh transport, not just its symptom): the
per-credential `HostKeyCallback` built from `ssh_known_hosts` *was* reaching the transport, but
go-git's `ssh.Transport.connect` separately derives `ssh.ClientConfig.HostKeyAlgorithms` by
scanning the OS-default `~/.ssh/known_hosts` locations whenever the caller leaves that field
empty — which the go-git `PublicKeys` auth type always does. On any host (or isolated test run)
without a populated `~/.ssh/known_hosts`, that unrelated fallback failed with "unable to find any
valid known_hosts file" before the correct, already-wired `HostKeyCallback` ever got a chance to
verify anything.

Fixed in `internal/gitrepo/transport.go`: `sshClientConfig` now fills `HostKeyAlgorithms` from the
same known_hosts-backed callback already built from `SSHAuth.KnownHosts`, via go-git's own
`knownhosts.HostKeyAlgorithms` helper — no file I/O, no `$HOME` dependency. Host-key verification
itself is unchanged and was not relaxed; the wrong-host-key negative in `internal/gitrepo`'s own
suite (real ssh handshakes against Gitea) still fails loudly. `internal/gitrepo`'s suite now also
runs with `$HOME` pointed at a throwaway temp dir for its entire `BeforeSuite`, so this class of
bug fails on every run rather than only on a machine that happens to lack `~/.ssh/known_hosts`.

The e2e `ssh` auth-kind scenario's permanent skip has been removed (`e2e/gitops_test.go`) so the
compose stack proves the fix for real; this row moves to fully FIXED once that e2e run is green in
the wave 3 gate. `pat` and `github_app` already pass end to end.

---

## 3. Unbuilt / gated features

### F5 — S3 sandbox simulation · **implemented 2026-08-20; both enablement gates CLOSED 2026-08-21**

VB-1 M7 (§6.4). Built and working: the simulation transform, the `shepherd-simulator` service with
capture harness and synthetic sources, the run API (migration 0007, cross-replica `RunWorker`), the
sandbox-run UI, the `sim` compose profile and the `sandbox-sim` e2e scenario.

**ENABLED BY DEFAULT in the Helm chart as of v0.0.1** (product decision, 2026-08-21, after both
gates closed): B-CONTAIN-1 is fixed and red/green-proven in compose (bind-address hardening +
`P-shepherd-deny` probes), NetworkPolicy enforcement is verified in a real cluster
(`e2e/k8s/simulator_containment_test.go` — all seven Layer B probes plus the kill probe), and
B-CONCAT, which blocked fan-in graphs from running at all, is fixed. The chart ships
`simulator.enabled: true`, auto-wires shepherd's `config.simulator` to the chart's own simulator
Service (an explicit `config.simulator` block wins), and documents the off-switch
(`simulator.enabled: false` — proven by chart_test.go and the kind suite's upgrade-to-disabled
spec). The default render is asserted to ship the simulator WITH its default-deny NetworkPolicy,
never without. The compose stacks keep their opt-in `sim` profile deliberately — the default
`make e2e` exercises the simulator-absent path — and shepherd's viper default stays false
(environments opt in via config; the chart is what opts production in). B-CONTAIN-2 remains a
documented local-dev-only caveat.

Containment posture, stated honestly: **the transform bounds credentials; the network bounds
reachability.** Static analysis cannot bound where a relabel rule steers a scrape — a rule writes
`__address__` at runtime with no host token in the rendered text. The static gate built to catch
that (rule P5) was deleted because it was simultaneously permeable and refused 5 of 6 ordinary
graphs.

Full findings with evidence: **`docs/archive/reviews/s3-sandbox-security-findings.md`**.

### F-SIGNAL-SERVE — role enforcement did not cover the live agent serve path · **CLOSED 2026-08-22**

W1 originally wired signal/role enforcement into `internal/mgmtapi`'s write-time paths only.
`internal/agentapi.Service`'s lazy recompute — the path a real collector's `GetConfig` poll takes
when `serve_cache` is dirty — called `merge.Assemble` without `WithRoleEnforcement`, so a
role-mismatched pipeline could reach a live collector inside that window.

**Closed in the same session that found it.** `internal/agentapi/service.go` now passes
`merge.WithRoleEnforcement`, and G6 is proven on that specific path rather than on the easier one:
`internal/agentapi/service_test.go`'s "does not serve a metrics pipeline to a logs collector
through the dirty-window path" drives a real `GetConfig` poll inside the dirty window and fails,
red-run proven, if enforcement is removed from the agent path alone.

This entry stayed open here for a day after the code was fixed, which a final review caught. That
is the mirror of R4: a status document claiming a live security gap that no longer exists costs
reader trust the same way a claimed-but-unwired control does.

### F-REVISIONS — revision diff and restore are not buildable yet · **medium**

`shepherd.mgmt.v1.PipelineRevision` carries only `revision`/`changed_by`/`changed_at`/`change_note`
— **not** the revision's contents. So a diff is impossible and restore cannot repopulate the editor;
the Restore button raises "Revision contents are not exposed by the API yet" and there is no
`RestoreRevision` RPC. The revision *list* works and is covered. Needs `contents` on the proto plus
a `RestoreRevision` procedure before any UI work.

### F-CONTRIB — collector detail does not show contributing pipelines · **low**

Served config is shown, but nothing links back to the pipelines that produced it, so there is no way
to get from "this collector runs X" to "because pipeline Y matched". The merge engine already knows
the contributing set.

---

## 4. Smaller follow-ups

- [ ] **TypeScript 7 migration (deferred 2026-09-11).** Dependabot #25 (typescript 7.0.2) fails
  typecheck: `tsconfig.json` `baseUrl` is removed (TS5102), and without it the test files lose the
  Node globals (`node:fs`, `node:path`, `__dirname`: TS2591) and `tests/fixtures/schema-fixture.ts`
  gains an implicit-any error. Needs explicit `types` and module settings in tsconfig; Dependabot
  ignores TypeScript majors until then. `@types/node` is pinned to the Node 24 runtime line and its
  majors are ignored too (Dependabot #21 wanted 26 against a Node 24 image).
- [ ] **React 19 migration (deferred 2026-09-11).** Dependabot #19 (react/react-dom 19.2.8,
  @types/react 19.2.18) breaks this app: `tests/specs/states.spec.ts` (route chunk failure
  fallback) throws React error #306, and `@xyflow/react` wire drags no longer produce edges
  (`visual-linking`, `visual-inspector` fan-in reorder, `visual-drafts` discard). Typecheck also
  needs `React.JSX.Element` in `web/src/routes/router.tsx`. `.github/dependabot.yml` ignores React
  majors until a planned migration; 18.x minors and patches still flow.
- [ ] Overlay entries scaffolded by `make schema` carry `needs_review: true` and need an editorial
      pass on the next Alloy bump
- [ ] `go.mod` carries a vestigial `github.com/lib/pq` line via testcontainers' own test dependency.
      No package we build or test imports it; `govulncheck` is clean
- [x] **The `github.com/docker/docker` Dependabot alerts are dismissed as unreachable, not fixed.**
      Re-verified 2026-08-25 (previously 2026-08-22, when the note below said six alerts; two have
      since been marked fixed upstream and four were dismissed). Each claim was re-checked rather
      than carried forward:
      - **Not in the build graph.** `go list -deps ./...` compiles **zero** packages from
        `docker/docker`. `go mod why` puts it behind the *test* binary of
        `golang-migrate/v4/database/pgx/v5` → `dhui/dktest`, so nothing we compile, test, or ship
        links it.
      - **No fix exists to take.** `v28.5.2+incompatible` is still the newest version published on
        that module path. `github.com/docker/docker/v29` now exists as a *separate* module path, but
        nothing in our chain has moved to it: `dhui/dktest v0.4.6` — the latest — still requires
        `docker/docker v28.3.3+incompatible`, and we are already on the newest `golang-migrate`
        (v4.19.1) and the newest dktest.
      - **`govulncheck ./...` reports 0 reachable vulnerabilities** (1 in a required-but-uncalled
        module).
      Dismissed with reason `not_used` rather than left standing open, because four permanently
      unfixable alerts train everyone to ignore the alert list, and the next one may be real.
      **Re-open and re-check if `golang-migrate`/`dktest` moves to `docker/docker/v29`** — a version
      bump there is the thing that would make this actionable.
- [x] **RESOLVED (2026-08-25): the `test-fullstack` flake was a false-positive Postgres
      healthcheck, fixed in v0.2.1.** `pg_isready` with no `-h` connects over the UNIX SOCKET, and on
      a first-time volume the postgres entrypoint runs a TEMPORARY server that listens on the socket
      only (`listen_addresses=''`) to execute the init scripts — logging "database system is ready to
      accept connections" while no TCP listener exists. Measured from a fresh container's log: the
      socket goes ready at `07.161`, the real IPv4 listener appears at `07.703`, and a client caught
      in between is recorded in the log as `FATAL: the database system is shutting down`. That ~550ms
      window is what `shepherd-init` fell into.
      It only reproduces on a FRESH volume, which is why it looked intermittent and never appeared
      locally: `test-fullstack` runs `down -v` and always initialises, while `make dev` reuses the
      volume and skips initdb entirely.
      Fixed by checking over TCP (`-h 127.0.0.1`) in all three places the pattern appeared — the dev
      and e2e compose stacks and the Kubernetes readiness probe in the k8s e2e fixtures, where the
      same false positive would have marked a pod Ready while clients through the Service still got
      refused. Note `internal/testutil/postgres.go` had already avoided this the other way round, by
      waiting for the "ready" log to appear TWICE; the knowledge was in the repo and the compose
      files never got it.
      **The first recorded guess here was wrong** — it blamed a race between the healthcheck and
      init's connection, when the healthcheck was measuring a different server altogether. Kept
      visible rather than quietly overwritten: the lesson is that "plausible mechanism" and "verified
      mechanism" are different things, and the log that settled it was available the whole time.
      `make test-fullstack` now dumps `compose ps -a` and 200 lines per service when the STACK fails
      to start (not only when the specs fail), which is what made the diagnosis possible.
- [ ] `make e2e-sim` cannot be run as a single invocation locally — the installed ginkgo CLI is
      version-mismatched (2.32.0 vs the module's 2.32.1). Its steps run individually
- [ ] Helm's Kubernetes containment is asserted at `helm template` text level only. A template
      assertion is not a probe — nothing dials from inside a real cluster's simulator Pod the way
      `P-deny-ip` does in compose

---

## 5. Explicitly out of scope (spec §19)

Per-org agent tokens; webhook-triggered git sync; OpAMP; editing git-sourced pipelines in the UI; a
multi-wizard framework beyond the one wizard; Alloy version-matrix testing; front-channel SSO
logout; horizontal-scale coordination beyond stateless replicas + Postgres.

---

## 6. Test infrastructure reference

- **Backend**: Ginkgo v2 + Gomega; testcontainers Postgres (Docker required for `make test`);
  `make test` / `make e2e` (compose: postgres, mock-oauth2, mockmsft
  Graph+ADO mock with `/__fixture` injection, shepherd, real Alloy)
- **Frontend**: Vitest units; Playwright mocked suite (route interception, no MSW); Playwright
  fullstack suite against the real dev stack (`make test-fullstack`), including `walkthrough.spec.ts`
  which walks every route asserting no console errors, failed requests or blank pages
- **Cross-cutting**: shared Go↔TS golden corpus, read directly from `internal/visual/testdata/corpus/`
  by both sides (W5-07 dropped the synced `web/src` copy `make generate-corpus` used to produce —
  `web/src/visual/renderTS.test.ts` resolves the Go directory by relative path instead, so the two
  cannot drift out of sync by construction); Makefile guards
- **CI**: lint, build, guards (incl. helm-lint), generated-drift, test, web, test-ui,
  test-fullstack, e2e-egress (containment probes, paths-filtered on PRs); scheduled: schema-verify
  (weekly), e2e-k8s (weekly)
- **The repository is public, so standard-runner Actions minutes are not billed; GitHub still
  runs every job separately**, so eight parallel CI jobs consume ~22 job-minutes for ~6 of wall
  clock, and a slow, noisy CI costs signal even when it costs no money. Measured 2026-08-21: one heavy
  development day burned 819 minutes. Three controls keep that in range — `paths-ignore` so a
  docs-only change never starts CI, a sub-minute `changes` job gating the two most expensive jobs
  (test-fullstack ~7 min, generated-drift ~5 min) on whether their own inputs moved, and weekly
  rather than nightly scheduling for the kind suite. **Reduce redundant executions, never
  coverage**: every gate still runs when its inputs change, and everything runs locally for free.

### A standard this repo now holds itself to

Two classes of defect recurred often enough to be worth naming:

1. **Silent-by-construction failures.** A dropped React Flow change type, a self-skipping spec, a
   containment key nobody probes — each reported success while covering nothing. Every control needs
   a test that *fails when the control is removed*; assert the observable consequence, not the
   configuration.
2. **`alloy validate` is not `alloy run`.** Validate accepts configs the real binary refuses at
   evaluation — that is how every committed golden came to describe a config Alloy cannot run (M13).
   Renderer output must be exercised against a running Alloy, not just validated.
