# Red–green proofs

Convention (from `docs/visual-builder-design-VB1.md` and the testing rules): when a fix or
feature lands, record a short proof here — the failing state before the change ("red run")
and the passing state after ("green run"). One file per fix, named for the behavior proved.

Proofs written before 2026-08-19 live in `docs/archive/proofs/`; they document work that is
complete. New proofs go here.

The four proofs here cover shipped controls and are **deliberately not archived**: Go source and
CI workflows cite these paths directly (`internal/simulate/transform.go`,
`internal/visual/render.go`, `.github/workflows/e2e.yml`, and others), so moving them would break
citations that exist to point a reader at the evidence. Same reason `git-provider-design.md`
stayed at the top level.

Since 2026-08-21 most new proofs are recorded **at the control itself** — a comment on the test
naming the exact one-line revert and the observed failure — rather than as a separate file. That
convention comes from `docs/gateway-tier-plan.md` §8, and it exists because a proof kept next to
the assertion cannot drift away from it. Either form is acceptable; the requirement is that the
red run was actually executed and its output recorded verbatim.
