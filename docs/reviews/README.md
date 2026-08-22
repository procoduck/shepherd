# Reviews — live findings and decision records

One document lives here. Everything else — the 2026-08-19 deep review, and as of 2026-08-22 the
S3 containment findings and the B-CONTAIN-1 decision record — is **implemented** and has moved to
[`docs/archive/reviews/`](../archive/reviews/).

| Document | Why it is still live |
|---|---|
| [`canvas-framework-evaluation.md`](canvas-framework-evaluation.md) | **Decision record.** Why the canvas uses React Flow, and the controlled-mode contract the canvas now depends on. Read this before changing `CanvasPane`'s node/edge projection — the contract is easy to break silently. |

Two documents moved to the archive on 2026-08-22, because both describe work that is finished:

- [`archive/reviews/s3-sandbox-security-findings.md`](../archive/reviews/s3-sandbox-security-findings.md)
  — the 2026-08-20 adversarial review of S3 sandbox containment. Every finding carries a status
  line and all are closed. **Archived because its headline conclusion is now false**: it says the
  feature "must stay disabled", and the sandbox has shipped **enabled by default** since v0.0.1
  (F5 in `docs/project-status.md`). A document that reads as a current instruction while stating
  the opposite of what ships is the exact hazard the archive exists to contain.
- [`archive/reviews/b-contain-1-bind-hardening.md`](../archive/reviews/b-contain-1-bind-hardening.md)
  — the decision record for B-CONTAIN-1's fix (Option C, bind-address hardening), shipped
  2026-08-21. Kept as the record of why the rejected options lost.

---

## The 2026-08-19 deep review — closed

Three independent fresh-context reviews found the visual builder could not produce a working
pipeline, and that the test suite could not detect it: every layer validated against hand-written
fixture schemas whose port model was the inverse of the shipped artifact.

**All ten priority items are implemented.** The assessments are archived as the historical record
of why the builder is shaped the way it is:

- [`archive/reviews/schema-generation.md`](../archive/reviews/schema-generation.md) — extractor → artifact → overlay → serving
- [`archive/reviews/graph-model-and-validation.md`](../archive/reviews/graph-model-and-validation.md) — port identity, connectivity, validation layers, both generators
- [`archive/reviews/canvas-ux-and-forms.md`](../archive/reviews/canvas-ux-and-forms.md) — canvas layout, palette, inspector forms, diagnostics

What closed them, because each was a distinct class of defect worth remembering:

| Was | Now |
|---|---|
| Fixture schemas inverted the shipped port model; corrupting the artifact left suites green | Every spec loads the embedded artifact through the registry |
| Nested blocks unrepresentable — 69.6% of the config surface unreachable | Rendered, and editable in the inspector |
| Nothing deletable; minimap empty; multi-input handles collided | One root cause — controlled mode without `applyNodeChanges`. See the decision record above |
| Form values all strings; renderer quoted them regardless of declared type | Typed serialization |
| Graphs did not round-trip — load re-parsed the generated text | `wizard_state` is the source of truth on load |
| L1 false-positive on 42 components | Exemption derived from the real artifact |
| `/api/schema/current` served `immutable, max-age=86400` | `no-cache` with ETag retained — the page had been reading a day-old schema, which is why edges vanished |
| Self-skipping specs went green exactly when a feature was missing | Every `test.skip()` removed; five vacuous tests deleted |
| 47 unnamed ports | 0 unnamed (314 total) |
| `make schema-verify` referenced in three docs but absent | Exists; its overlay guards run in CI via `go test`, and the upstream-clone artifact diff runs on the weekly `schema-verify` scheduled workflow |

One correction recorded at the time is worth keeping: the reverse-direction edge that "could not
be made to render" was never a React Flow bug. The graph model correctly encodes Alloy's
"arguments reference exports"; the canvas presents left-to-right dataflow and inverts at render
time (`wireOrient.ts`). That is a layout convention, not a defect.

## Open questions still worth answering

1. The canvas presents left-to-right dataflow and inverts to Alloy's export→argument direction at
   render time. It works, but it is a convention worth confirming deliberately rather than
   inheriting by default.
2. What is the minimum component set the builder must support *well*? 184 is unrealistic to curate
   at once, and the overlay's `needs_review` entries are the visible edge of that.
