# VB-1 execution ledger

## Status: M6 complete ✅ — post-real-browser review findings recorded, M7 blocked pending cross-cutting fixes

## Milestones
- M1 ✅ (commit 0f0e1f8) — Schema unification: alloy v1.18.1, 184 components, GET /api/schema, 7.3 tests 13/13
- M2 ✅ (commit ec6ca4a) — Canvas core: React Flow, Palette, Node, Inspector, L1, store, Vitest 27/27, Playwright 74
- M3 ✅ (commit c0511fe) — Codegen + gate: Go renderer, TS renderer, corpus 9 entries, REST endpoints
- M4 ✅ (commit 0fec1a1) — Read-only graph view + recreate as visual
- FIX ✅ (commit 98b4648) — reuseExistingServer=false
- REVIEW ✅ (commit 0d719c5) — Adversarial review findings documented (4 CRITICAL, 13 HIGH, 14 MEDIUM)
- REMEDIATION ✅ (commit c6788ba) — All C/H/M findings fixed: Go 22/22 · Vitest 34/34 · Playwright 74/74
- M5 ✅ (commit 7285e60 + e2e525a) — S1 + S2: flow check overlay, relabel/log simulate endpoints + UI: Go 30/30 · Vitest 34/34 · Playwright 81/81
- M5-REVIEW ✅ (commit e2e525a) — Adversarial review findings fixed: MustNewRegexp panic, missing limits, multiline not_simulated, S1 BFS disabled-node halt, misleading checkbox removed
- M6 ✅ (commit 9c1d370 + cfc225d) — Upgrade machinery: upgrade-check, review UI, needs_upgrade filter, overlay migrations: Go 39/39 · Vitest 34/34 · Playwright 88/88
- M6-REVIEW ✅ (commit cfc225d) — Adversarial review findings fixed: body limit, unknown-version 400, null-prop required-attr check, block props excluded from attr_removed, Discard wiring clarified, Accept note added
- REAL-BROWSER ✅ (commits 740edd1, 1e9f1c9) — Real-browser session found 7 bugs; all fixed: Handle position:relative, no fitView, screen→flow coords, palette blocking, module-level counter, vacuous linking tests, scroll isolation + connection highlights + compatible palette filter
- POST-BROWSER-REVIEW ⏳ — Three adversarial reviewers found 20 additional findings (5 CRITICAL, 10 HIGH, 5 MEDIUM). Recorded below. Fix required before M7.
- M7 ☐ — S3 (blocked pending post-browser-review fixes)
- M8 ☐ — Hardening

## Post-real-browser adversarial review findings (2026-08-18)

Three reviewers ran concurrently: R1 = Visual Node UI, R2 = Alloy Registration/Metadata, R3 = Dev Seed.

### CRITICAL findings (must fix before M7)

**R2-C1 — Status string never matches UI color map (always grey UNKNOWN)**
- Stored as `"RemoteConfigStatuses_APPLIED"` (proto enum). UI does `.toUpperCase()` → still wrong prefix.
  STATUS_COLORS keys are `"APPLIED"`, `"APPLYING"`, `"FAILED"` — none ever match.
- Fix: strip `"RemoteConfigStatuses_"` prefix at API boundary OR normalise before storage.

**R2-C2 — GetConfig overwrites hostname with UUID on every poll**
- `RegisterCollector` saves `req.Msg.Name` (hostname). Every `GetConfig` call (every 10s) calls
  `upsertCollectorInstance(req.Msg.Id, req.Msg.Id, ...)` — Name arg is the UUID. SQL ON CONFLICT
  overwrites. After first poll every instance shows its UUID as its name.
- Fix: preserve existing non-empty name during GetConfig, OR resolve hostname from `attrs["collector.name"]`.

**R2-C3 — API returns no instance metadata; detail page always shows blank**
- `collectorResponse` has no `alloy_version`, `os`, `last_seen`, `name`. Detail page TypeScript-casts
  but the wire data is absent — all metadata fields are `undefined` at runtime.
- Fix: add a latest-instance join to GetCollector and the list response.

**R1-C1 — Visual builder rendered inside padded shell — needs fullscreen layout**
- Shell `<main>` has `overflow-y-auto` + `max-w-[1400px] mx-auto px-6 py-6`. The visual builder
  scrolls with the page instead of filling the viewport. Palette (256px) + inspector (360px) +
  sidebar (240px) + padding (96px) = 952px consumed before canvas gets anything on a 1280px screen.
- Fix: visual builder routes must bypass the shell's padded content wrapper; render at h-screen.

**R3-C1 — All seeded pipelines have empty matchers — nothing ever served to Alloy**
- Merge engine treats `matchers=[]` as "match nothing". `base-metrics` is enabled but served config
  is always empty. Alloy reports `APPLIED` for the empty config — looks healthy but is not.
- Fix: seed at least one pipeline with `matchers=['cluster="prod-eu-1"', 'role="metrics"']` and
  valid complete Alloy content (full forwarding chain).

### HIGH findings

**R1-H1 — Unnamed port handle IDs (`"0"`) break connections for real schema components**
- Ports without `prop`/`export` get ID `String(i)`. `isValidConnection` looks up by `p.export === sourceHandle`
  — `undefined !== "0"`, so connection is rejected. Affects real components (e.g. beyla.ebpf).
- Fix: centralise handle-ID resolution used by both rendering and validation.

**R1-H2 — All nodes re-render on every wire drag (connectingFrom in every node's data)**
- `connectingFrom` is in `rfNodes` useMemo deps. 50+ nodes all get new data objects on drag start/end.
- Fix: move connection-drag state to a narrow store selector; derive per-node highlighting without
  copying state into every node's data object.

**R1-H3 — Bottom drawer open by default eats 256px on short screens**
- On 768px screen: topbar 56 + banner 48 + toolbar 44 + drawer 256 = 404px gone; 364px canvas.
- Fix: drawer should default to collapsed (open=false).

**R2-H1 — Reconnect after sweeper stale-mark stays inactive**
- After `MarkStaleInstancesInactive` sets `remote_config_status='inactive'`, next poll's
  `UpsertCollectorInstance` updates last_seen but leaves status. If poll has no RemoteConfigStatus
  payload, status never clears. False outage indicators persist.
- Fix: treat successful GetConfig as liveness recovery; clear stale marker on reconnect.

**R2-H2 — Collectors list shows only Cluster+Role — no status, version, last-seen**
- `remote_config_status` is in the API response but CollectorsPage table renders only two columns.
- Fix: add Status (with colour), Last Seen, and Version to the list table.

**R3-H1 — `data-eng` org sorts first alphabetically — app looks empty on first login**
- ListOrgs orders by name. `data-eng` has no clusters, pipelines, or collectors. First-time
  developer sees entirely blank pages.
- Fix: make `platform-org` the dev default, or persist last-selected org, or add first-run banner.

**R3-H2 — Stub instances duplicate real Alloy registrations — ambiguous status**
- Seed creates `dev-instance-metrics` etc. Real Alloy containers register with different IDs.
  UpsertCollectorInstance keys on instance ID, so stubs stay alongside live instances.
- Fix: remove stub instances from seed; let compose Alloy containers populate the DB.

**R3-H3 — `base-metrics` has no forwarding chain — functionally inert**
- `prometheus.exporter.self "seed" {}` with no scrape/remote_write. Even if stage-1 valid,
  sends nothing anywhere. No connection to seeded destinations.
- Fix: seed a complete chain (exporter → scrape → remote_write) with valid destination URL.

**R3-H4 — Seed bypasses revision/audit — revisions page empty for seeded data**
- `CreatePipeline` called directly; normal API path creates revision 1 + audit event.
- Fix: use API-equivalent operations or explicit seed helpers that create revision 1 + audit entry.

**R3-H5 — No seeded demo visual pipeline**
- Visual builder has no example graph to open and explore. User must build from scratch.
- Fix: seed one visual pipeline with persisted wizard_state graph, valid Alloy content, matchers.

### MEDIUM findings

**R2-M1 — `local_attributes` blob stored but never exposed to operators**
- Custom attrs (region, env, etc.) stored in DB; API never returns them. Fix: expose in detail response.

**R1-M1 — No re-fit-view after first fit**
- FitOnFirstNodes fits once (hasFit=true); subsequent nodes added go off-screen.
- Fix: also trigger fit on importGraph; or expose a "fit graph" keyboard shortcut more prominently.

**R2-M2 — Metadata overwritten if later poll omits fields**
- Upsert replaces alloy_version/os/local_attributes even if absent from new request. No merge.
- Fix: preserve known non-empty values when new request omits them.

**R3-M1 — Seed prints "staging (unclaimed)" but both clusters are now claimed**
- Fix: update the fmt.Printf to reflect actual state.

**R3-M2 — No matcher→collector relationship visible**
- User doesn't understand why served config is empty. preview-matches endpoint exists but not surfaced.
- Fix: show "matches N collectors" on each pipeline row; link pipeline→matched collectors.

## Next action

Fix all CRITICAL findings before resuming M7. Recommended order:
1. R2-C1: status normalisation (small, high-impact)
2. R2-C2: hostname preservation in GetConfig
3. R2-C3: instance metadata in API response
4. R1-C1: visual builder fullscreen layout
5. R3-C1: seed pipeline with real matchers + valid content
Then address HIGH findings, then proceed to M7.

## SPEC-CONFLICT / DEFERRED / BLOCKED entries

- BLOCKED: make schema generation (network) → committed-artifact mode
- DEFERRED: UpdatePipelineParams has no wizard_state field; wizard_state not persisted on PUT.
  Deferred to M8 hardening (add UPDATE wizard_state query + wire).
- SPEC-CONFLICT resolved: otel-three-signals now uses correct OTel components (was Prometheus)

## Notes on platform monitoring architecture

Sourced from `<internal platform repository>/tests/snapshot/fixtures/monitoring` (2026-08-18).
Full detail in `docs/platform-monitoring-architecture.md`.

Key findings relevant to Shepherd:
- Production Alloy fleet has exactly 4 roles: `logs`, `metrics`, `receiver`, `singleton` — matches
  Shepherd's `validRoles` exactly. No changes needed.
- None of the production Alloy instances have a `remotecfg` block yet. They are driven by static
  ConfigMaps via the Alloy Operator. Shepherd integration is the prerequisite — `remotecfg` blocks
  get added during rollout AFTER Shepherd ships.
- Secrets pattern: configs use `remote.kubernetes.secret` for Azure AD OAuth2 credentials.
  Shepherd serves these blocks verbatim; Alloy resolves secrets locally from k8s. Shepherd does
  not need secret management.
- Primary matcher key: `cluster` label (e.g. `cluster="prod-eu-1"`). Secondary: `role`.
- Realistic demo seed pipeline for `alloy-metrics`:
  `prometheus.exporter.self` → `prometheus.scrape` → `prometheus.remote_write` with
  `external_labels = { cluster = "prod-eu-1" }` and matchers `['cluster="prod-eu-1"', 'role="metrics"']`.
- The `remotecfg` gap is intentional — this is what Shepherd enables. Sequencing:
  1. Shepherd ships (M1–M8) → 2. rollout adds remotecfg to Alloy configs → 3. configs migrate in.

## Session log

- 2026-08-18: Session 1 — M1–M4 implemented, adversarial reviews run, all findings fixed.
  Final state: Go 22/22 · Vitest 34/34 · Playwright 74/74. Starting M5.
- 2026-08-18: Session 2 — M5 implemented (S1 flow overlay + S2 relabel/log simulate).
  Added github.com/prometheus/prometheus v0.314.0. Final state: Go 30/30 · Vitest 34/34 · Playwright 81/81.
- 2026-08-18: Session 3 — M6 (upgrade machinery), real-browser session (7 bugs fixed),
  three adversarial reviews (20 findings: 5C, 10H, 5M). Final test state: Go 39/39 · Vitest 34/34
  · Playwright 87/87. M7 blocked pending post-browser-review CRITICAL fixes.
  Platform monitoring architecture explored via <internal platform repository> fixtures:
  4 Alloy roles confirmed (logs/metrics/receiver/singleton), no remotecfg in production yet —
  remotecfg is what Shepherd enables; rollout happens after Shepherd ships. Full notes in
  docs/platform-monitoring-architecture.md.
