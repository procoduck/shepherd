# Shepherd — project ledger

> **The single live status document.** Baseline re-verified 2026-08-20 by running everything below,
> not by reading a summary. Completed rounds live in `docs/archive/` — do not start a second ledger.

## Document map

| Live | Purpose |
|---|---|
| `docs/project-status.md` | this ledger — verified baseline, open bugs, unbuilt features |
| `docs/spec.md` | authoritative product/build specification (§ numbers referenced below) |
| `docs/visual-builder-design-VB1.md` | visual builder design — M1–M8 built; §6.4 (S3) is the live spec for the disabled sandbox feature |
| `docs/reviews/` | **live findings only**: S3 containment criticals, and the canvas decision record |
| `docs/dev-guide.md` | running the dev stack |
| `docs/frontend-testing.md` | three-layer frontend test strategy |
| `docs/platform-monitoring-architecture.md` | target-fleet reference notes |
| `docs/kind-test-environment-plan.md` | **steps 1–3 in progress, §5 Layer B done**: kind-based Kubernetes test environment — NetworkPolicy enforcement (probed), Helm deploy, LGTM delivery |
| `docs/gateway-tier-plan.md` | **in progress** (all 11 workstreams built as of 2026-08-22; W1–W3 done, the rest awaiting review gates R1/R2/R3/R6 — R5 was resolved 2026-08-22 — see its §9): multi-session plan for the tenant-aware gateway tier, the beacon and outcome verification, signal/role enforcement, the chart-values generator, teams/scoped identity and the agent (MCP) interface — 11 workstreams with its own step ledger (§9), 15 conformance gates (§6), review gates (§7) and the actor model (§3a) |
| `docs/proofs/` | red–green proofs for current work |
| `docs/archive/` | finished work, kept as the record of why things are the way they are |

---

## 1. Verified baseline (2026-08-20)

Every line re-run today.

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
- **All 12 SPA routes** as of this baseline — walked in Chrome with a console/network collector: zero console errors,
  zero JS exceptions, zero failed requests
- **Visual builder** — schema-driven palette (184 components, 314 named ports, 0 unnamed),
  draw.io-style connection dragging, delete/undo/redo, minimap, save/load with matchers
- **GitOps** — Gitea credential + repo link syncing, status `ok` (F9 shipped)
- **Wizard, audit, overview, admin CRUD, org switcher** — all functional
- **S3 sandbox run** — a live run completes in ~20s with 21 captured series and 3/3 healthy
  components. **Disabled by default** — see F5 below.

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
- F5's defaults remain off; enabling is now a product decision (see F5).

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

### F9-a — `ssh` auth kind fails in the compose stack · **medium**

Works in `internal/gitrepo`'s own suite (real ssh handshakes against Gitea, covering wrong-host-key
and wrong-passphrase negatives). In the e2e compose stack it fails with go-git's "unable to find
any valid known_hosts file" — the per-credential `HostKeyCallback` is not reaching the transport
even though `ssh_known_hosts` is populated. The e2e case is skipped with that reason.
**Host-key verification must NOT be relaxed to make it pass.** `pat` and `github_app` pass end to end.

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

- [ ] Overlay entries scaffolded by `make schema` carry `needs_review: true` and need an editorial
      pass on the next Alloy bump
- [ ] `go.mod` carries a vestigial `github.com/lib/pq` line via testcontainers' own test dependency.
      No package we build or test imports it; `govulncheck` is clean
- [ ] **Six open Dependabot alerts on `github.com/docker/docker` (2 high, 2 medium at last count)
      cannot currently be fixed, and this is not neglect.** Checked 2026-08-22: `v28.5.2+incompatible`
      is the newest version published on that module path, and the advisories cover `<= 28.5.2` and
      `< 29.3.1` — 29.x is not published there, so there is no version to move to. We are already on
      the latest `golang-migrate` (v4.19.1), which is what drags it in, and only through the *test*
      binary of `database/pgx/v5`. It appears in **zero** packages of our build graph (`go list -deps
      ./...`), so nothing we compile, test, or ship links it; `govulncheck` reports 0 reachable
      vulnerabilities. Revisit when golang-migrate/dktest moves to a patched docker, and re-check
      rather than assuming — the alert count going up does not necessarily mean new exposure.
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
- **Cross-cutting**: shared Go↔TS golden corpus (`internal/visual/testdata/corpus/` mirrored to
  `web/src/visual/__fixtures__/corpus/`, `make generate-corpus`); Makefile guards
- **CI**: lint, build, guards (incl. helm-lint), generated-drift, test, web, test-ui,
  test-fullstack, e2e-egress (containment probes, paths-filtered on PRs); scheduled: schema-verify
  (weekly), e2e-k8s (weekly)
- **Actions minutes are budgeted (3000/month) and GitHub bills every job separately**, so eight
  parallel CI jobs cost ~22 billed minutes for ~6 of wall clock. Measured 2026-08-21: one heavy
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
