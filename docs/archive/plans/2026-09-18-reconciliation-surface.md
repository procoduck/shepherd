# Reconciliation surface — declared vs served vs observed (W6, #110)

_2026-09-18. Ships in the next release; move to `docs/archive/plans/` then._

## Problem

`CollectorDetailPage` shows the served config, but nothing surfaces **drift** —
whether what a collector's role _declares_, what Shepherd _serves_ it, and what
the collector is _observed_ running actually agree. The comparison engine
already exists (`internal/reconcile.Compare`, pure and tested) but is called
only by its own tests: it is built, not wired.

## What already exists (confirmed)

- `internal/reconcile.Compare(declared, served, observed) ([]Finding, error)` —
  pure three-way comparison. Two finding kinds: `role_signal_mismatch`
  (declared↔served) and `unserved_component_observed` (served↔observed).
- The data is per-instance: `beacon_inventory` is keyed
  `(principal, instance_label, component_name)`, so "observed" resolves per
  collector instance, not just per token (the tracking note was imprecise).

## The observed-linkage gap (decided 2026-09-18: build the precise linkage)

`beacon_inventory` is keyed `(principal, instance_label, component_name)`, and
`collector_instances` stores neither — so a beacon row can't be attributed to a
collector, and a shared per-cluster token makes a principal match misattribute
components between role-collectors. Fix it at the source: Shepherd renders each
collector's baseline pipeline and knows the collector id there, so **stamp the
collector id into the beacon write** and carry it through to storage.

- **`internal/beacon` baseline render** — the baseline's existing
  `prometheus.relabel "beacon"` gains a constant-label rule stamping
  `shepherd_collector_id = <collector id>`. `AppendBaseline` / the baseline
  config take the collector id; `serve.ComputeServed` threads `coll.ID`.
- **`beacon.Project`** extracts the `shepherd_collector_id` label and returns it
  alongside the instance label.
- **`beacon_inventory`** gains a nullable `collector_id` column (migration
  `0025`); the beacon handler stores it. A new `ListBeaconInventoryByCollector`
  query backs the observed lookup. Rows written before a collector picks up the
  new baseline carry no id and simply don't appear as observed yet (absence is
  never disagreement).

## Approach — on-demand, read-time

A new **`FleetService.GetReconciliation(org_id, collector_id)`** (org-reader,
like `GetServedConfig`) builds the three inputs on demand and returns `Compare`'s
findings:

- **Declared** — the collector's enforced `role` + `local_attributes`.
- **Served** — the pipelines actually assembled into the collector's served
  config (post role/signal enforcement, replicating the match+enforce loop since
  neither `serve.Result` nor `merge.AssembleResult` exposes the included set),
  each as `ServedPipeline{Name, ControllerPath: "pipe_"+merge.SanitizeName(Name),
  Signals: signals.Derive(contents)}`.
- **Observed** — `ListBeaconInventoryByCollector(collector_id)` rows
  (`component_name` → `ControllerPath`, `healthy`, `Stale` when `last_seen` is
  past the 5-minute sweeper TTL), **scoped to Shepherd's managed namespace**
  (`pipe_*` controller paths). This is deliberate: `pipe_<name>` is the verified
  identity a declared pipeline reports under, so a `pipe_*` path observed but not
  served is real, actionable drift (a disabled/deleted pipeline a collector is
  still running). Root-level components (Alloy's root controller, where the
  baseline and any BYO top-level components run) are not individually
  distinguishable via `alloy_component_controller_running_components` and are out
  of scope here — filtering to `pipe_*` also avoids false-flagging the baseline,
  with no dependency on an unverified root controller_path string.

`FleetService` gains `schema` + `beaconBaseline` deps (it has neither today).
A source adapter builds the inputs from the store; `Compare` stays pure. Proto:
a `Finding` message (kind/sources/summary/pipeline_name/controller_path/stale)
and `GetReconciliationResponse{ repeated Finding findings }`.

## UI

A **Reconciliation** tab on `CollectorDetailPage`: "In sync" when there are no
findings, otherwise a list grouped by kind, each finding showing its summary and
the two disagreeing sources, with the stale caveat where present. Read-only.

## Tests

- Go: an adapter/handler integration test seeding a collector + pipelines +
  beacon rows and asserting the findings for each kind (and the clean case).
- Web: a `CollectorDetailPage` spec for the Reconciliation tab (in-sync and a
  drift finding), with mock handlers.

## Out of scope

- Auto-remediation. This surface reports drift; acting on it stays manual.
- Fleet-wide drift rollup — this is per-collector, matching the issue.
