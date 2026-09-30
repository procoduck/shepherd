# Experimental components as an org setting (#114)

_2026-09-17. Ships in the next release; move to `docs/archive/plans/` then._
_Decided 2026-09-11 (remediation D-series)._

## Problem

The visual builder gates experimental Alloy components behind a store flag
`allowExperimental` that is **hardcoded `false`** (`web/src/visual/store.ts`),
so experimental components are hidden in the palette for everyone and there is
no way to permit them. The only enforcement (`l1.ts` `experimental_gated`) is
client-side and advisory — nothing stops a crafted graph from rendering and
being saved server-side.

## Approach

Make it a real per-org setting with an **authoritative server-side render
gate**, and wire the existing client flag to it.

### Storage + proto
- Migration `0024_org_allow_experimental`: `orgs.allow_experimental_components
  boolean NOT NULL DEFAULT false`.
- `orgs.sql` `UpdateOrg` sets the column; every `SELECT *` picks it up.
- `admin.proto`: `Org.allow_experimental_components` (10),
  `UpdateOrgRequest.allow_experimental_components` (6).
- `me.proto`: `OrgMembership.allow_experimental_components` (5) — the shape the
  SPA actually reads (a regular member can't `ListOrgs`).

### Server render gate (authoritative)
- `visual.ComponentSchema` gains `Stability` (the artifact already carries
  per-component `"stability"`; Go just dropped it).
- New pure `visual.ExperimentalNodes(doc, payload)` → the enabled nodes whose
  component is `stability == "experimental"`. Table-tested.
- `VisualService.Render` / `Validate`: when the org's toggle is off, append an
  L1 `experimental_gated` render diagnostic per experimental node — the same
  channel a label collision uses, so the builder's save already blocks on it.
  Default-deny if the org can't be loaded.

### Wiring the client flag
- `rpc_me.go` populates `OrgMembership.allow_experimental_components`.
- The visual store gets `setAllowExperimental`; `VisualBuilderPage` sets it from
  the current org's flag on load, so the palette shows experimental components
  only for a permitted org.
- `AdminOrgsPage` edit form gets an "Allow experimental components" checkbox,
  sent on `UpdateOrg`.

## Tests
- Go: `ExperimentalNodes` table tests; a `Render` integration test (toggle off →
  `experimental_gated`; toggle on → clean) and `UpdateOrg` round-trips the flag.
- Web: an AdminOrgsPage toggle spec, and a palette spec (experimental component
  present with the org flag on, absent with it off). Me mock carries the flag.

## Out of scope
- Per-pipeline or per-user overrides — the gate is org-wide.
- `CreateOrgRequest` flag — new orgs default off; admins flip it via UpdateOrg.
