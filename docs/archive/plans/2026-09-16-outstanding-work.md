# Outstanding work — implementation plan & sequence (2026-09-16)

Written after v0.8.0 (collector OIDC) shipped. This is the execution sequence for **all** remaining
work; `docs/project-status.md` stays the single live status ledger and `docs/gateway-tier-plan.md`
holds the gate sign-offs. Each item below was **state-confirmed against the code on `main`**, not
inferred from the changelog — the "Confirmed" line says how.

Migration numbering: `0021` (agent_identities) and `0022` (beacon_principal) are taken. **Next free
migration is `0023`.** Any item needing a migration below assumes it renumbers from there.

---

## Wave 0 — Housekeeping (no decisions; clears the board)

- **PR #83 (agent-OIDC design doc) never landed on `main`.** `docs/plans/` is empty; the design
  record for a now-shipped feature sits on an open branch. Merge it (or archive it under
  `docs/archive/plans/`), then close. *Confirmed: `gh pr list` shows #83 OPEN; `ls docs/plans/` empty.*
- **Post-release ledger PR.** Move `docs/project-status.md` §1 baseline to v0.8.0 / release run
  35112915878; add the v0.8.0 History line; archive nothing new. *Confirmed: §1 still says "v0.7.0".*
- **Dependabot triage:** #63 (go minor+patch ×5), #64 (codeql-action sha), #56 (npm web minor+patch
  ×3). Review + merge if green. *Confirmed: all three OPEN.*
- **Contributor PR #54 (collector inventory labels).** Two problems: (1) it adds migration `0021`,
  now **collided** with agent_identities — must renumber to `0023`; (2) the last review left one ask
  (set-label audit `value`/`previous_value`). Decide: take it over vs. nudge the contributor.
  *Confirmed: #54 OPEN, mergeable UNKNOWN; `0021` occupied.*

## Wave 1 — Small, already-decided, high leverage (no new sign-off)

Each was **signed 2026-09-11** (`project-status.md` §4) — settled, just unbuilt.

1. **`shepherd_build_info` gauge** (labels `version`, `commit`) in `internal/metrics`.
   *Confirmed unbuilt: no `build_info` in `internal/metrics/`.* — S, no proto.
2. **Editor Format + Validate buttons.** New `FormatPipeline` RPC over `alloy fmt` (proto change
   pre-approved) wired to a Format button; explicit Validate button beside the idle-debounced
   validation. *Confirmed unbuilt: no `FormatPipeline` in proto or `rpc_pipeline.go`.* — M.
3. **REST-shim deprecation.** `Deprecation` header on every shim route + changelog notice; removal
   one release later. *Confirmed unbuilt: no deprecation header in `mgmtapi`.* — S.
4. **SSO read-only agent-audience/grant display** (finishes the agent-OIDC surface). Add
   `agent_audience` / `agent_required_role` (read-only) to the `OidcSettings` proto message + show
   them in `AdminAuthPage` beside the bindings section. *Confirmed unbuilt: `OidcSettings` has 20
   fields, none agent-related.* — S.

## Wave 2 — Product surfaces for existing backends (RPC done, UI missing)

Both backends are complete and gate-cleared; only the SPA surface is missing.

5. **Tenant routes UI** (W4, cleared by R1): create/list/rotate/revoke, with the
   identifier-not-authorizer caveat + gateway-rate-limit guidance on the docs site.
   *Confirmed: `tenant_route.proto` + `rpc_tenant_route.go` implement all four RPCs; no page/route.* — M.
6. **Service-accounts UI** (W10 remainder): create/list/revoke with role tier + one-time secret
   reveal. *Confirmed: `service_account.proto` implements List/Create/Revoke; no page, no transport
   client.* — M.

## Wave 3 — Gated builds (build, then sign / then unblock)

7. **Per-service-account rate limit** — the **only** open R6 condition. Server-side, keyed on the
   service-account id, in `internal/mgmtapi/machine_auth.go`. Landing this unblocks the MCP interface
   (W11). *Confirmed: nothing rate-limit in `machine_auth.go`; propose-audit already exists.* — M.
8. **MCP interface reachability (W11)** — once (7) lands, R6 is fully satisfied. Decide whether to
   ship `cmd/shepherd-mcp` (still deliberately out of the linux server archives — see gateway plan
   §7) or keep build-from-source. — S/decision.
9. **Receiver tier build (R3)** — chart Deployment + Service + NetworkPolicy (gateway the only
   ingress), tested off-switch, real-Alloy pass-through tenancy e2e; **then bring R3 back to sign.**
   *Confirmed: no receiver template in `deploy/helm/shepherd/templates/`.* — L.
10. **Reconciliation surface (W6)** — per-collector declared vs served vs observed drift. Needs the
    observed channel keyed per instance (beacon rows are per-token today). *Confirmed: no
    observed/drift UI in `CollectorDetailPage`.* — L (backend + UI).
11. **Onboarding artifacts page (W7)** — "connect an app" snippets for a tenant route. Depends on
    Wave 2 #5. — M.
12. **Chart-values generator UI (W9) + G10** kind-suite gate (NOTES.txt CNI/NetworkPolicy warning). — M/L.

## Wave 4 — Deferred / needs a decision

13. **Agent-OIDC mode 2** (IdP-authoritative org): `agent_trust_org_claim` / `agent_org_role_prefix`
    resolution tier. *Confirmed unbuilt.* Build now or keep deferred until a deployment needs it.
14. **Experimental components as an org setting** (migration + proto field + server render gate),
    replacing the hardcoded client flag. Decided in principle 2026-09-11. — M.
15. **B1 — `APPLIED` status can mean "polled with the served hash", not "loaded OK".** Needs a live
    Alloy v1.19.2 reproduction, then one of three options (walkthrough plan §3). — decision + M.
16. **B2 — wizard warnings channel.** `CommitResult`/`RenderWizardResponse` carry no `warnings`
    field (a `proto/` change, deferred). Approve to proceed. — S.
17. **B3 — `Chart.yaml` `kubeVersion` floor.** `>=1.25.0-0` global vs. `cnpg.enabled` needs
    `>=1.29.0-0`. Raise the floor (chart minor) or keep + document. — decision.
18. **Graph diff for visual pipelines** (revision diff is text-only today). — M.

### Smaller follow-ups (tracked, low priority — see `project-status.md` §4)
F-CONTRIB (collector→contributing pipelines link), NOTES.txt `https://` cosmetic, sandbox run-window
vs scrape-interval, typed `Role`/`Source` enums, canvas keyboard a11y, nested `bindings[]`
rendering, kind-suite plan steps 3 & 5, overlay `needs_review` editorial pass, vestigial
`lib/pq`/`docker/docker` dep noise.

---

## Suggested release grouping

- **v0.9.0** — Wave 1 (build_info, Format/Validate, REST-shim deprecation notice, SSO display) +
  Wave 2 (tenant routes UI, service-accounts UI). All decided, no gate risk.
- **v0.10.0** — Wave 3 gated builds: SA rate limit → MCP unblock; receiver tier → R3 sign-off.
- **v0.11.0+** — reconciliation, onboarding, chart-values UI, then Wave 4 as prioritised.

## Decisions taken 2026-09-16 (were blocking the sequence)

- **Start point:** Wave 1 + Wave 2 → **v0.9.0**. First release scope is build_info, Format/Validate,
  REST-shim deprecation, SSO display, tenant-routes UI, service-accounts UI.
- **PR #54:** **take it over** — rebase onto main, renumber migration `0021 → 0023`, add the
  set-label audit `value`/`previous_value` fix, push so it can merge.
- **B3 kubeVersion floor:** **raise the global floor to `>=1.29.0-0`** (a chart minor; fold into the
  next chart release). Update the database docs to drop the conditional-floor caveat.
- **Agent-OIDC mode 2:** **build it now** — the IdP-authoritative resolution tier
  (`agent_trust_org_claim` / `agent_org_role_prefix`), its config keys, docs, and enforcement tests.
  Slot after Wave 1+2 (its own slice), promoting item 13 out of "deferred".

R3 (receiver tier) and R6 (SA rate limit → MCP) remain *decided* (build-then-sign /
rate-limit-then-MCP) — they need building, not a new sign-off, and stay in Wave 3.
