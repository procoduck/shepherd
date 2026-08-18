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

## extract.go

~300 lines. Injected as `cmd/shepherd-schema-dump/main.go` inside the alloy checkout. Performs:

1. Blank-imports `internal/component/all` to register all components.
2. Iterates `component.AllRegistrations()`.
3. Reflects the `Args` struct via `alloy:"name,attr|block"` tags.
4. Reads port types from `internal/component/metadata.ForComponent(name)`.
5. Emits the artifact JSON (key-sorted via `jq -S` in the shell wrapper).

**Invariant:** `_meta.components_total` equals `len(components)`. Non-zero exit if violated.
