# Archive — completed work

Documents here describe work that is **finished and shipped**. They are kept as the
historical record of why things are the way they are; they are no longer maintained and
must not be treated as current instructions. Anything still outstanding from these
documents has been carried forward into `docs/project-status.md`, which is the single
live ledger.

| Document | What it covered | Shipped in |
|---|---|---|
| `api-contract-design.md` | Design for the protobuf/Connect management-API contract with REST shims for external integrations | pre-reset history, 2026-08-19 — see the note below |
| `visual-builder-refinement.md` | UI/UX review of the visual pipeline builder: design tokens, draw.io-style connection dragging, working save/load, one-command Alloy schema bumps | pre-reset history, 2026-08-19 — see the note below |
| `vb1-progress.md` | VB-1 execution ledger for milestones M1–M6 and the three adversarial review rounds. Every CRITICAL/HIGH/MEDIUM finding it records is now fixed | closed out 2026-08-19; remaining VB-1 milestones (M7 sandbox simulation, M8 hardening) live in `docs/project-status.md` |
| `proofs/` | 17 red–green proofs, one per fix, written while the work was done | 2026-08-17 → 2026-08-18 |
| [`../git-provider-design.md`](../git-provider-design.md) (live, not archived — kept at top level because source files cite its § numbers) | Standard-git GitOps with pluggable provider auth (basic/pat/ssh/ado_sp/github_app), tested against a real Gitea. Shipped as F9; the compose-stack `ssh` scenario (F9-a) was fixed 2026-09-10 and runs green in `make e2e` | 2026-08-19; F9-a 2026-09-11 |
| `plans/2026-09-11-f-revisions.md` | F-REVISIONS implementation plan: revision contents on the API, `RestoreRevision`, diff + Restore in the editor, migration 0019, with its §5a amendments | v0.6.0 (PR #53), archived 2026-09-15 |
| `plans/2026-09-14-walkthrough-fixes.md` | The sixteen findings of the v0.6.0 manual UI walkthrough, eight slices with disjoint file sets, and the §3 "Blocked" decisions (APPLIED semantics, wizard warnings on the proto, chart `kubeVersion` with CNPG) that the ledger still carries | v0.7.0 (PR #67), archived 2026-09-15 |
| `plans/2026-09-14-kind-dev-stack.md` | `make dev-kind`: the reusable single-node kind stack (Calico, CNPG, Gateway API + NGF, Gitea, mock OIDC), its K1–K6 slices and the section 1 contract the repocheck specs cite | v0.7.0 (PR #69), archived 2026-09-15 |
| `completed-2026-08-19.md` | The 2026-08-19 baseline round verbatim: seven bugs (B1–B7) and the features F1–F9 that closed with it | 2026-08-19 |
| `reviews/` | The three fresh-context deep reviews of the visual builder and schema pipeline. All ten priority items implemented; see `docs/reviews/README.md` for what closed each | 2026-08-19 → 2026-08-22 |
| `reviews/s3-sandbox-security-findings.md` | The 2026-08-20 adversarial review of S3 sandbox containment. All findings closed. **Archived 2026-08-22 because its conclusion is now false** — it says the feature must stay disabled, and the sandbox ships enabled by default since v0.0.1 | gates closed 2026-08-21; enabled in v0.0.1 |
| `reviews/b-contain-1-bind-hardening.md` | Decision record for B-CONTAIN-1's fix: bind-address hardening (Option C), with the rejected options' analysis | 2026-08-19 → 2026-08-20, gate closed 2026-08-21 |
| `reviews/2026-08-25-full-review-fixes.md` | Five parallel fresh-context reviews (auth/authz, chart/deployment, merge engine, API/data layer, frontend). Seven phases, all landed across four commits; F1.1 (`helm upgrade` silently dropping the database under cnpg+ESO) and F5.1 kill-probed by reverting the fix and watching the failure | `v0.3.4` (2026-08-25) |
| `reviews/2026-09-09-remediation.md` | The 2026-09-09 whole-project review's remediation record: decisions D1–D14, the eight parallel workstreams (CI/build, Go core, RBAC, sandbox, visual builder, UI shell, tests, docs), what landed and what was deferred. Archived once every deferred item had shipped | `v0.4.0` (2026-09-11); deferred items in `v0.5.0` (2026-09-11) |
| `completed-2026-09-11.md` | The ledger's own history through v0.5.0, moved out of `docs/project-status.md` verbatim: the 2026-08-20 baseline, the 2026-08-21/22 passes, the 2026-09 remediation summary, every fixed bug (B-CONTAIN-1, B-CONCAT, B-STAGEORDER, F9-a) and closed feature (F5, F-SIGNAL-SERVE) with its red-run evidence, and the closed follow-ups (React 19, TypeScript 7, Vite 8, request-gate auth, lazy editor, dismissed `docker/docker` alerts, the `test-fullstack` healthcheck flake) | `v0.0.1` → `v0.5.0` (2026-08-20 → 2026-09-11) |

## Where the live documents are

- `docs/project-status.md` — **the ledger**: current baseline, open bugs, unimplemented features,
  open follow-ups. Its history through v0.5.0 is `completed-2026-09-11.md` here
- `docs/spec.md` — the authoritative product/build specification
- `docs/visual-builder-design-VB1.md` — still live: the design specification for the visual
  builder, §6.4 for S3 sandbox simulation. **Corrected 2026-09-11**: this bullet used to say the
  sandbox "ships disabled by default with open containment criticals" — stale on both counts. Both
  enablement gates closed 2026-08-21 (B-CONTAIN-1, NetworkPolicy enforcement in a real cluster,
  B-CONCAT); the chart has shipped `simulator.enabled: true` since v0.0.1 (`docs/project-status.md`
  F5). Only the Go binary's own viper default stays `false` — the chart is what opts production in.
- `docs/reviews/` — one live document: React Flow's controlled-mode contract
  (`canvas-framework-evaluation.md`)
- `docs/gateway-tier-plan.md` — the multi-session plan for the gateway tier, beacon, teams and
  agent interface; its §9 ledger is where workstream status lives
- `docs/kind-test-environment-plan.md` — the kind suite plan (steps 3 and 5 still open) and, in §11,
  the reusable dev stack `make dev-kind` that shares its pins
- `docs/plans/` — dated per-PR implementation plans. A plan moves to `docs/archive/plans/` once its
  work has shipped in a tag, with the closing note carrying anything still open into the ledger.
  Empty since v0.7.0; Go test comments and the changelog cite the archived paths
- `docs/git-provider-design.md` — live because Go source cites its § numbers (see the table above)
- `docs/proofs/` — red–green proofs for shipped controls. **Not archived**, because Go source and
  CI workflows cite these paths directly (the same reason `git-provider-design.md` stayed live)
- `docs/dev-guide.md` — how to run the dev stack
- `docs/frontend-testing.md` — the three-layer frontend test strategy
- `docs/platform-monitoring-architecture.md` — target-fleet reference notes

## A note on the archived commit SHAs

This index used to cite commit SHAs for `api-contract-design.md`'s and
`visual-builder-refinement.md`'s "Shipped in" column (`d401cc4`, `4e502ac`, `27306d8`, `ff9541e`,
`4b88147`, `ffd5471`, `a57556d`, `d3ef41b`, `4318bc3`, `9e22d57`) — none of those SHAs appear
inside either document; they were added here, and none resolve
(`git cat-file -e <sha>^{commit}` fails for all ten). `vb1-progress.md` cites two more, and these
DO appear in that document's own text (`0f0e1f8`, `ec6ca4a`) — also unresolvable. All thirteen are
from a **pre-reset history that does not exist in this repository**.

**Corrected 2026-09-11**: this note previously named `11f4e16` as the commit this repository's
history starts at — `11f4e16` is itself one of the thirteen unresolvable pre-reset SHAs (it was
cited, uncorrected, in `docs/archive/reviews/canvas-ux-and-forms.md` too). The real root of this
repository's actual history is `b83d5d99` (`git rev-list --max-parents=0 HEAD`, 2026-08-18), and
its first tagged release is `v0.0.1` (2026-08-21) — those are the two anchors that actually
resolve. Treat every pre-reset SHA above as a label for sequencing within its own document, never
as a commit you can look up.
