# Archive — completed work

Documents here describe work that is **finished and shipped**. They are kept as the
historical record of why things are the way they are; they are no longer maintained and
must not be treated as current instructions. Anything still outstanding from these
documents has been carried forward into `docs/project-status.md`, which is the single
live ledger.

| Document | What it covered | Shipped in |
|---|---|---|
| `api-contract-design.md` | Design for the protobuf/Connect management-API contract with REST shims for external integrations | `d401cc4`, `4e502ac`, `27306d8`, `ff9541e` (2026-08-19) |
| `visual-builder-refinement.md` | UI/UX review of the visual pipeline builder: design tokens, draw.io-style connection dragging, working save/load, one-command Alloy schema bumps | `4b88147`, `ffd5471`, `a57556d`, `d3ef41b`, `4318bc3`, `9e22d57` (2026-08-19) |
| `vb1-progress.md` | VB-1 execution ledger for milestones M1–M6 and the three adversarial review rounds. Every CRITICAL/HIGH/MEDIUM finding it records is now fixed | closed out 2026-08-19; remaining VB-1 milestones (M7 sandbox simulation, M8 hardening) live in `docs/project-status.md` |
| `proofs/` | 17 red–green proofs, one per fix, written while the work was done | 2026-08-17 → 2026-08-18 |
| `git-provider-design.md` | Standard-git GitOps with pluggable provider auth (basic/pat/ssh/ado_sp/github_app), tested against a real Gitea. Shipped as F9; `ssh` in the compose stack remains open as F9-a | 2026-08-19 |
| `completed-2026-08-19.md` | The 2026-08-19 baseline round verbatim: seven bugs (B1–B7) and the features F1–F9 that closed with it | 2026-08-19 |
| `reviews/` | The three fresh-context deep reviews of the visual builder and schema pipeline. All ten priority items implemented; see `docs/reviews/README.md` for what closed each | 2026-08-19 → 2026-08-20 |

## Where the live documents are

- `docs/project-status.md` — **the ledger**: current baseline, open bugs, unimplemented features
- `docs/spec.md` — the authoritative product/build specification
- `docs/visual-builder-design-VB1.md` — still live: M1–M8 are built, and §6.4 remains the
  specification for S3 sandbox simulation, which ships **disabled by default** with open
  containment criticals
- `docs/reviews/` — live findings only: the S3 containment criticals, and the canvas decision
  record explaining React Flow's controlled-mode contract
- `docs/dev-guide.md` — how to run the dev stack
- `docs/frontend-testing.md` — the three-layer frontend test strategy
- `docs/platform-monitoring-architecture.md` — target-fleet reference notes

## A note on the archived commit SHAs

`vb1-progress.md` cites SHAs (`0f0e1f8`, `ec6ca4a`, …) from a pre-reset history that does
not exist in this repository — this repo's history starts at `11f4e16`. Treat those as
labels for sequencing, not as resolvable commits.
