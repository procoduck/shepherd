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
1. Regenerates the artifact into a temp dir (`SCHEMA_OUT_DIR`), leaving
   `overlay.json` untouched (`SKIP_RECONCILE=1`).
2. Normalises both sides with `jq -S 'del(._meta.generated_at)'` and diffs them.
   `generated_at` is the only non-deterministic field in the artifact — a naive
   `diff` would fail 100% of the time on exactly that one line. Everything else
   is byte-reproducible (key sorting via `jq -S` plus `sort.Strings(names)`).
3. Fails with the diff attached if they differ, or if no artifact is committed
   for the pinned version at all.

`run.sh` also honours `ALLOY_SRC=<path>` to reuse an existing checkout instead of
cloning, which turns a local verify from a ~4 GB clone into a ~40 s run.

An Alloy version bump PR must include: `versions.env` change + regenerated artifact + overlay entries for new components + a fleet stage-3 revalidation sweep.

## Overlay guards (CI-enforced)

- Every key in `schema/overlay.json` must exist in the artifact. (Hard fail — overlay cannot reference components that don't exist.)
- New artifact components with no overlay `category` land in the Advanced palette category with a CI **warning** (not failure — a new experimental component must never block a version bump).
- The discovery-stub map keys in the overlay must be `discovery.*` components present in the artifact.
- A component's `port_display_order` must name exactly the ports the artifact
  declares for it — every port once, and no name that does not exist. Without
  this the overlay rots inside a single Alloy version: nine entries pointed at
  22 non-existent port names before the guard existed.

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

Injected as `cmd/shepherd-schema-dump/main.go` inside the alloy checkout, with
`portmodel.go` copied in beside it as a second `package main` file. `extract.go`
carries `//go:build ignore` so this repo never compiles it; `portmodel.go` has no
alloy imports, so it *is* compiled and unit-tested here (`portmodel_test.go`,
run by CI's `go test ./...`).

What it does:

1. Blank-imports `internal/component/all` to register all components.
2. Iterates `component.AllNames()` + `component.Get(name)` (sorted).
3. Reflects the `Args` struct via `alloy:"name,kind[,optional]"` tags into
   `attributes` and nested `blocks`.
4. Derives ports from the `Arguments` and `Exports` struct tags.
5. Emits the artifact JSON (key-sorted via `jq -S` in the shell wrapper) plus a
   coverage summary on stderr.

**Invariant:** `_meta.components_total` equals `len(components)`. Non-zero exit if violated.

### Ports, names and roles

`inputs` are fields of the `Arguments` struct and `outputs` are fields of the
`Exports` struct. **That is not dataflow direction** — `prometheus.remote_write`
is a sink whose only port is the *export* `receiver`, and `prometheus.scrape` is
a metrics source whose ports are both *arguments*.

Every port therefore carries a `role`, which IS dataflow direction (visual
builder decision D1):

| | argument (`Arguments`) | export (`Exports`) |
|---|---|---|
| **data** wire (`discovery.Target`) | `accepts` | `produces` |
| **receiver** wire (`storage.Appendable`, `loki.LogsReceiver`, `otelcol.Consumer`, `pyroscope.Appendable`) | `produces` | `accepts` |

`produces` means data leaves the node there (drawn on the right); `accepts` means
data enters (drawn on the left). So `prometheus.remote_write.receiver` is an
export with role `accepts`, and `prometheus.scrape.forward_to` is an argument
with role `produces`. The canvas draws source → destination in both cases; the
renderer inverts where Alloy requires it. `RoleFor` in `portmodel.go` is the
whole rule, and it is table-tested.

**Port names are read from the alloy struct tags**, never from
`internal/component/metadata` — that package reports the *types* a component
accepts and exports but not the *names*, and a name-less port cannot be
referenced by a stored graph. The walk descends into tagged single blocks and
through `,squash` embeds, so a consumer that lives inside `output {}` still gets
a name. Since `metadata`'s five types are exactly the five in `GoTypeWireMap`,
this walk is a strict superset of what the metadata fallback could report, and
there is no fallback any more.

#### Nested port names

A port inside a block is named by its dotted path and also carries that path
pre-split:

```json
{"prop": "output.metrics", "path": ["output", "metrics"],
 "type": "otel.metrics", "role": "produces", "cardinality": "list"}
```

The naming follows the struct tags literally: the block's tag name, then the
attribute's tag name. For every `otelcol.*` component that is
`otelcol.ConsumerArguments` inside `alloy:"output,block"`, giving
`output.metrics` / `output.logs` / `output.traces` — which is exactly how the
corpus already addressed them. `faro.receiver` has its own `OutputArguments`
(`output.logs` as `loki.logs`, `output.traces` as `otel.traces`);
`beyla.ebpf`, `otelcol.receiver.splunkhec` and `otelcol.receiver.fluentforward`
follow the same shape with their own subsets.

**The renderer must not emit a dotted name as an attribute** — Alloy attribute
names may not contain a dot. A port with `len(path) > 1` is written as nested
blocks: `output { metrics = [...] }`. That is what `path` is for.

Ports inside *repeatable* blocks are deliberately not collected: a port on a
block that may appear N times has no stable address. The extractor counts any it
skips and prints the count (currently 0).

#### Per-signal OTel wire types

`otelcol.Consumer` is refined to `otel.metrics` / `otel.logs` / `otel.traces`
when the field addressing it is signal-specific, which covers every consumer
*argument* in alloy. It stays `otel.any` only for `otelcol.ConsumerExports.Input`
— one consumer handling all three signals, so the polymorphism is real there.
Consumers of the schema must treat `otel.any` as compatible with every `otel.*`.

### Attributes, blocks and defaults

- `alloy:",squash"` embedded structs are flattened into the parent's surface at
  the same depth. This is where `loki.write`'s endpoint credentials
  (`basic_auth`, `authorization`, `oauth2`, `tls_config`) actually live; before
  squash support they were absent from the artifact entirely.
- `alloy:"x,enum"` (a repeatable one-of, e.g. `loki.process`'s stages,
  `remote.vault`'s auth) is expanded to one block per alternative named
  `<enum>.<alternative>` — `stage.docker`, `stage.json`, `auth.token` — which is
  how Alloy addresses them.
- Every block carries `name`, `repeatable`, `required`, `attributes` and
  `blocks`; the last two are always arrays, never `null`, at every depth. The
  same normalisation applies to a component's own four list fields.
- `maxDepth` is 8, and recursion is additionally cut by a per-path type stack so
  a self-referential config type terminates on the cycle rather than the budget.
  A cut block is marked `"truncated": true` instead of silently ending. Exactly
  one site is truncated in v1.18.1: `loki.process` → `stage.match` → `stage`,
  whose `StageConfig` contains itself.
- Attribute types mirror Alloy's own `AlloyType`
  (`syntax/internal/value/type.go`): capsule, then `TextMarshaler` → string, then
  duration → string, then the Go kind. Two refinements the inspector needs are
  split back out: `duration` and `secret`.
- `default` is read by instantiating the type and calling `SetToDefault()` where
  the type implements `syntax.Defaulter`, then reading the field. Zero values are
  omitted (indistinguishable from unset) and **secrets never carry a default**.
  Durations are emitted as Alloy duration literals (`"200ms"`).
- `values` (enum choices) is derived by re-parsing the alloy package that
  declares a named string type and collecting that type's string constants —
  e.g. `discovery.relabel`'s `action`, `loki.source.file`'s
  `on_positions_file_error`. **Nothing is invented.** An attribute typed as a
  plain `string` whose valid values live only in prose or in a `Validate()`
  method carries no `values` at all, and none is fabricated for it. That is why
  the count is modest (34 attributes in v1.18.1) rather than universal; the
  remainder belongs in the overlay if it is ever wanted.
- `input_type` is set on an attribute that is also a port, carrying its wire type
  (`targets`, `prom.metrics`, ...). The L1 validator uses it to skip the
  "required attribute missing" check for something the canvas wires up rather
  than something a user types.
