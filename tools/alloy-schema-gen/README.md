# tools/alloy-schema-gen

Generates `schema/alloy-v<X>.json` by cloning grafana/alloy at the fleet-pinned tag and running `extract.go` INSIDE that checkout.

## Why inside the checkout?

Go's `internal/` visibility rule prevents importing `grafana/alloy/internal/*` from outside the alloy module. The extractor must be injected into the cloned checkout to access the component registry and metadata packages.

## Usage

```bash
make schema          # requires: git, go, jq, network access to ALLOY_REPO
```

Reads `ALLOY_VERSION` from `deploy/versions.env`. CI can override `ALLOY_REPO` with your organisation's mirror if applicable.

## Committed-artifact mode

When network access is unavailable (local dev, air-gapped), the committed `schema/alloy-v<X>.json` is used directly. **Never run `make schema` as part of an application build** — builds must be hermetic.

## CI discipline

`make schema-verify` (Alloy-bump PRs + weekly cron):
1. Runs `make schema` to regenerate the artifact.
2. Diffs the result against the committed artifact via `diff`.
3. Fails (with diff attached) if they differ.

An Alloy version bump PR must include: `versions.env` change + regenerated artifact + overlay entries for new components + a fleet stage-3 revalidation sweep.

## Overlay guards (CI-enforced)

- Every key in `schema/overlay.json` must exist in the artifact. (Hard fail — overlay cannot reference components that don't exist.)
- New artifact components with no overlay `category` land in the Advanced palette category with a CI **warning** (not failure — a new experimental component must never block a version bump).
- The discovery-stub map keys in the overlay must be `discovery.*` components present in the artifact.

## Bumping the Alloy version

`deploy/versions.env` is the single source of truth for the Alloy version —
`ALLOY_VERSION` (the git tag `run.sh` clones) and `ALLOY_IMAGE` (the Docker
image tag used by the app images and the e2e Alloy container) must agree.
Nothing else needs editing by hand: `internal/version/alloy_gen.go` is
generated from `ALLOY_VERSION` (`make gen-alloy-version`, wired into both
`make generate` and `make schema`), and `make check-docker` fails the build
if a Dockerfile's `ARG ALLOY_IMAGE` default ever drifts from `versions.env`.

The bump procedure is: edit `ALLOY_VERSION` in `deploy/versions.env` →
`make schema` → review `needs_review` entries → commit.

`make schema` does three things in order:

1. Clones `grafana/alloy` at the pinned tag and runs `extract.go` inside the
   checkout to regenerate `internal/schema/artifacts/alloy-v<X>.json`
   (see "extract.go" below).
2. Reconciles `internal/schema/artifacts/overlay.json` against the freshly
   generated artifact (`reconcile.go`, see "Overlay reconciliation" below).
3. Prints a summary of what the reconciliation added and removed.

An Alloy version bump PR must include: the `versions.env` change, the
regenerated artifact, overlay entries for new components, and a fleet
stage-3 revalidation sweep.

## Overlay reconciliation

`reconcile.go` (`go run reconcile.go <artifact.json> <overlay.json>`,
invoked by `run.sh` after generation) keeps `overlay.json`'s `components`
map in sync with whatever the artifact says exists, without a human having
to notice the diff by hand:

- A component that's new in the artifact gets a skeleton overlay entry —
  `{"category": <heuristic>, "doc": "", "needs_review": true}` — so it's
  never silently invisible in the palette. The category is a best-effort
  guess from the component's dotted path (`discovery.*` → sources,
  `*.relabel`/`*.process*` → transform, `*.remote_write`/`*.write`/
  `otelcol.exporter.*` → destinations, `remote.*`/`local.*` → config, else
  advanced) — always confirm or correct it, then drop `needs_review`.
- A component that's gone from the artifact has its overlay entry deleted
  (an orphaned overlay key fails the `ValidateOverlay` CI guard).
- Entries for components present in both are left completely untouched —
  editorial fields (`doc`, `icon`, `port_display_order`, `discovery_stub`,
  ...) are never overwritten by a bump.
- `wire_types` and `categories` are untouched; only `components` is
  reconciled.

It exits nonzero only on I/O errors (the overlay file can't be read, parsed,
or written back) — additions and removals are expected, routine output, not
a failure. Its heuristic and merge behavior are unit-tested in
`reconcile_test.go` against in-memory fixtures (no network, no real clone).

## extract.go

~300 lines. Injected as `cmd/shepherd-schema-dump/main.go` inside the alloy checkout. Performs:

1. Blank-imports `internal/component/all` to register all components.
2. Iterates `component.AllRegistrations()`.
3. Reflects the `Args` struct via `alloy:"name,attr|block"` tags.
4. Reads port types from `internal/component/metadata.ForComponent(name)`.
5. Emits the artifact JSON (key-sorted via `jq -S` in the shell wrapper).

**Invariant:** `_meta.components_total` equals `len(components)`. Non-zero exit if violated.
