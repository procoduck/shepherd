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
| `docs/kind-test-environment-plan.md` | **steps 1–3 in progress**: kind-based Kubernetes test environment — NetworkPolicy enforcement, Helm deploy, LGTM delivery |
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
- **All 12 SPA routes** — walked in Chrome with a console/network collector: zero console errors,
  zero JS exceptions, zero failed requests
- **Visual builder** — schema-driven palette (184 components, 314 named ports, 0 unnamed),
  draw.io-style connection dragging, delete/undo/redo, minimap, save/load with matchers
- **GitOps** — Gitea credential + repo link syncing, status `ok` (F9 shipped)
- **Wizard, audit, overview, admin CRUD, org switcher** — all functional
- **S3 sandbox run** — a live run completes in ~20s with 21 captured series and 3/3 healthy
  components. **Disabled by default** — see F5 below.

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

---

## 2. Open bugs

### B-CONTAIN-1 — the sandbox can reach the control plane · **critical** (S3 only)

`shepherd` is attached to `sim-internal` in **both** compose files, alongside `simulator`.
`internal: true` denies egress to the internet; it does nothing about *neighbours*. The sandbox
scrapes Shepherd's unauthenticated metrics port and the data is returned to the user in run
results. Fix: split the networks so Shepherd reaches the simulator's control API somewhere the
sandbox is not.

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

### B-CONCAT — `CheckEndpoints` refuses the expression the renderer now emits · **high**

The M13 fix made `render.go` emit `array.concat(...)` when several discovery sources fan into one
scrape. `simsvc.CheckEndpoints` refuses expressions outright, so any such graph cannot run in the
sandbox. The guard's premise ("a transformed config contains no calls") became false when M13 was
fixed, and nothing tests the two against each other.

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

### F5 — S3 sandbox simulation · **implemented 2026-08-20, MUST STAY DISABLED**

VB-1 M7 (§6.4). Built and working: the simulation transform, the `shepherd-simulator` service with
capture harness and synthetic sources, the run API (migration 0007, cross-replica `RunWorker`), the
sandbox-run UI, the `sim` compose profile and the `sandbox-sim` e2e scenario.

**Disabled by default and must stay so** until B-CONTAIN-1 closes and NetworkPolicy enforcement is
verified in a real cluster (B-CONTAIN-2 is local-dev-only — see above). `simulator.enabled`
has no viper default (false); both compose files default `SHEPHERD_SIM_ENABLED` to false; Helm ships
`simulator.enabled: false`.

Containment posture, stated honestly: **the transform bounds credentials; the network bounds
reachability.** Static analysis cannot bound where a relabel rule steers a scrape — a rule writes
`__address__` at runtime with no host token in the rendered text. The static gate built to catch
that (rule P5) was deleted because it was simultaneously permeable and refused 5 of 6 ordinary
graphs.

Full findings with evidence: **`docs/reviews/s3-sandbox-security-findings.md`**.

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
  (weekly), e2e-k8s (nightly)

### A standard this repo now holds itself to

Two classes of defect recurred often enough to be worth naming:

1. **Silent-by-construction failures.** A dropped React Flow change type, a self-skipping spec, a
   containment key nobody probes — each reported success while covering nothing. Every control needs
   a test that *fails when the control is removed*; assert the observable consequence, not the
   configuration.
2. **`alloy validate` is not `alloy run`.** Validate accepts configs the real binary refuses at
   evaluation — that is how every committed golden came to describe a config Alloy cannot run (M13).
   Renderer output must be exercised against a running Alloy, not just validated.
