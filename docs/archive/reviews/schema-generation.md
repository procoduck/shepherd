# Review: Alloy component schema generation, testing, and delivery

Reviewer: independent engineering review, 2026-08-19.
Scope: `tools/alloy-schema-gen/`, `internal/schema/`, `internal/schema/artifacts/*`,
the `/api/schema/*` endpoint, `web/src/visual/schemaAdapter.ts` + consumers,
`deploy/versions.env` / `internal/version/`, and every test that touches them.
Method: read the code, cloned `grafana/alloy@v1.18.1`, ran the extractor against it,
diffed the result against the committed artifact, ran the Go and web suites, and
deliberately corrupted inputs to see what fails. Every claim below is backed by a
`file:line` or a command I actually ran.

---

## Verdict

The **generator is better than the code that consumes it**. I re-ran the whole
pipeline from a fresh `grafana/alloy@v1.18.1` clone and the output is
byte-identical to the committed artifact (modulo one timestamp line), covers
184/184 registered components with no omissions, and — as of commit `8a3c7c5`
— finally carries real Alloy port names. That part is sound.

The single biggest problem is that **nothing downstream has been migrated to the
schema the server actually serves, and no test would ever tell you.** The Go
render tests (`internal/visual/render_test.go:21`), the frontend fixture
(`web/tests/fixtures/schema-fixture.ts`), all nine corpus goldens and thirteen
Playwright specs run against a hand-written 9–12 component schema whose port
model is the *inverse* of the generated artifact's. I rendered all nine committed
corpus graphs against the real embedded artifact: **0 of 9 reproduce their
golden, and in every single one the metrics/logs hop is silently dropped**,
producing a config that parses but collects nothing. Separately, corrupting real
port names in the committed artifact (`prometheus.scrape.inputs[0].prop`,
`prometheus.remote_write.outputs[0].export`) leaves `go test
./internal/schema/... ./internal/visual/... ./internal/mgmtapi/...` **fully
green**. The artifact's most load-bearing data has zero test coverage, and the
suite that looks like it covers it is testing a fiction.

Secondary but material: the CI enforcement the docs describe (`make
schema-verify`) **does not exist**, `go test ./internal/...` in CI excludes
`./tools/...` so the generator's only real unit tests never run, and the
"edit one version, run one command" bump story is falsifiable in under a minute.

---

## How it works today

### Data flow

```
deploy/versions.env  (ALLOY_VERSION=v1.18.1, ALLOY_IMAGE=grafana/alloy:v1.18.1)
        │
        ├─ make gen-alloy-version ─────────► internal/version/alloy_gen.go
        │      (Makefile:241-243)              const AlloySchemaVersion = "alloy-v1.18.1"
        │
        └─ make schema (Makefile:252-253) ──► tools/alloy-schema-gen/run.sh
                 │
                 ├─ 1. git clone --depth 1 --branch $ALLOY_VERSION grafana/alloy   (run.sh:28)
                 ├─ 2. sed-strip `//go:build ignore` from extract.go, inject it as
                 │       cmd/shepherd-schema-dump/main.go inside the checkout      (run.sh:35)
                 ├─ 3. go run ./cmd/shepherd-schema-dump | jq -S .                 (run.sh:39)
                 │       └──► internal/schema/artifacts/alloy-v1.18.1.json  (584 KB, 184 components)
                 └─ 4. go run reconcile.go <artifact> <overlay>                    (run.sh:45)
                         └──► rewrites internal/schema/artifacts/overlay.json
                              (adds needs_review skeletons, deletes orphans)

internal/schema/embed.go   //go:embed artifacts   ──► schema.Embedded (embed.FS)
internal/schema/registry.go
        New(fsys, version.AlloySchemaVersion)  → parses overlay.json once
        Get("alloy-v1.18.1")                   → reads artifact, deepMerge(artifact, overlay),
                                                 sha256 → ETag, memoised in r.cache
        │
        ├─ internal/mgmtapi/schema.go  GET /api/schema/current, /api/schema/{version}
        │        └─ web/src/visual/schemaAdapter.ts fetchSchema() → Zustand store
        │             → PipelineNode.tsx handles, CanvasPane.tsx wire validation,
        │               InspectorPanel.tsx prop editor, renderTS.ts client-side render
        │
        └─ internal/mgmtapi/rpc_visual.go renderGraph() / rpc_pipeline.go
                 marshal(merged) → visual.SchemaPayload → visual.Render(doc, schema)
                 → Alloy config text
```

### What the extractor actually does (`tools/alloy-schema-gen/extract.go`)

It runs *inside* the alloy checkout because `grafana/alloy/internal/*` is not
importable from outside the module (`README.md:5-9`). It:

1. blank-imports `internal/component/all` to populate the global registry
   (`extract.go:20`);
2. iterates `component.AllNames()` + `component.Get(name)` (`extract.go:151-158`)
   — note the README at line 71 says `AllRegistrations()`, which is not what the
   code calls;
3. maps `featuregate.Stability` → `ga` / `public-preview` / `experimental`
   (`extract.go:240-251`);
4. reflects the `Args` struct via `alloy:"name,kind[,optional]"` tags to produce
   `attributes` and nested `blocks` (`walkStruct`, `extract.go:290-357`);
5. derives `inputs` from Arguments-struct fields whose Go type is a wire type and
   `outputs` from Exports-struct fields (`portsFromStruct`, `extract.go:71-110`),
   with `metadata.ForComponent(name)` as a *type-only, name-less* fallback
   (`extract.go:185-201`);
6. emits `default_snippet` as the literal `fmt.Sprintf("%s \"example\" {}\n", name)`
   (`extract.go:204`) and `doc: ""` (`extract.go:161`) — docs are the overlay's job.

**Crucial semantics to internalise:** `inputs` means "fields of the `Arguments`
struct", `outputs` means "fields of the `Exports` struct". This is *not* dataflow
direction. `prometheus.remote_write` is a sink but has `outputs:
[{export:"receiver"}]` and `inputs: []`; `prometheus.scrape` is a source of
metrics but has `inputs: [targets, forward_to]` and `outputs: []`. That is
faithful to Alloy — you wire a scrape to a remote_write by writing
`forward_to = [prometheus.remote_write.sink.receiver]` — and `internal/visual/render.go:272-299`
implements exactly that (it writes `<input.Prop> = [<from-node>.<from-port>]`).
The generated artifact and the renderer agree. Nothing else in the repo does.

### The overlay (`internal/schema/artifacts/overlay.json`, 41 KB)

Purely editorial; it adds **no** structural data. Field census across its 184
component entries:

| field | entries |
|---|---|
| `category` | 184 |
| `doc` | 184 (0 empty) |
| `icon` | 184 |
| `terminal_ok` | 170 |
| `discovery_stub` | 32 |
| `port_display_order` | 27 |
| `key_props` | 2 |
| `needs_review` | **0** |

Plus top-level `wire_types` (8 ids with colors/labels) and `categories` (5 with
colors/labels). `reconcile.go` only ever touches the `components` map
(`reconcile.go:56-58`).

### Version pinning

`deploy/versions.env` is `include`d by the Makefile with a bare `export`
(`Makefile:6-7`), so `ALLOY_VERSION` reaches `run.sh` and the injected extractor
as an environment variable. `make check-docker` (`Makefile:201-223`) enforces
that every `ARG` default in the three app Dockerfiles equals the corresponding
`versions.env` value. I ran it: `check-docker: OK`.

---

## Findings

### CRITICAL-1 — Every visual-builder test validates against a hand-written schema that contradicts the generated artifact

**Evidence.** Two stand-in schemas exist:

- `internal/visual/render_test.go:21-110` — `corpusSchema()`, 12 components,
  commented *"the shared test schema matching web/tests/fixtures/schema-fixture.ts"*.
- `web/tests/fixtures/schema-fixture.ts:1-95` — 9 components; imported by 13
  files (`web/src/visual/schemaAdapter.test.ts`, `web/src/visual/l1.test.ts`, and
  10 Playwright specs under `web/tests/specs/`).

Their port model is inverted relative to the artifact for every terminal
component:

| component | fixture (`schema-fixture.ts`) | real artifact (`alloy-v1.18.1.json`) |
|---|---|---|
| `prometheus.scrape` | out: `metrics` | out: **none**; in: `targets`, `forward_to` |
| `prometheus.remote_write` | in: `receiver` | **out**: `receiver`; in: none |
| `loki.source.file` | out: `logs` | out: **none**; in: `targets`, `forward_to` |
| `loki.write` | in: `receiver` | **out**: `receiver`; in: none |
| `otelcol.receiver.otlp` | out: `output.metrics/logs/traces` | in: **1 unnamed** `otel.any` port |
| `otelcol.exporter.otlp` | in: `input.metrics/logs/traces` | out: `input` (single, `otel.any`) |

I wrote a throwaway probe (`internal/zzschemareview/probe_test.go`, since
deleted) that loads `schema.Embedded`, merges the overlay, and runs
`visual.Render` over every committed corpus graph:

```
########## minimal-scrape.graph.json  goldenMatch=false stage1Valid=true diags=0
discovery.kubernetes "k8s" { role = "pod" }
prometheus.scrape "app" { targets = [discovery.kubernetes.k8s.targets] }
prometheus.remote_write "sink" {
}
```

**0 of 9 corpus graphs match their golden against the real schema, and in every
one the metrics/logs hop vanishes** — the edge references a port the artifact
does not declare, `render.go:273-283` finds no `in.Prop` match, and the edge is
silently discarded with *zero diagnostics*. `kitchen-sink` additionally emits
`prometheus.relabel "drop_internal" { forward_to = [prometheus.scrape.app.metrics] }`,
referencing an export that does not exist in Alloy — that config parses at stage 1
and dies at stage 2/3 on the agent.

Worse, the committed goldens are not even syntactically valid Alloy. Running
`validate.Stage1` over the nine files in `internal/visual/testdata/corpus/`:

```
golden bindings-secret.golden.alloy     stage1Valid=false  [{7 11 expected block label, got [}]
golden otel-three-signals.golden.alloy  stage1Valid=false  6× {attribute names may only consist of a single identifier with no "."}
golden (7 others)                       stage1Valid=true   ← but semantically invalid Alloy
```

The `otel-three-signals` failure is structural: the fixture's dotted port names
(`input.metrics`) can never be rendered by `render.go`'s
`lw.writef("  %s = [%s]\n", in.Prop, …)` writer, because Alloy attribute names
cannot contain a dot. The "intended" model encoded in the fixtures is
unimplementable as written.

**Falsification test.** I patched the committed artifact
(`jq '.components["prometheus.scrape"].inputs[0].prop = "TOTALLY_BOGUS_PORT" |
.components["prometheus.remote_write"].outputs[0].export = "BOGUS2"'`) and ran:

```
ok  shepherd/internal/schema   0.491s
ok  shepherd/internal/visual   (cached)
ok  shepherd/internal/mgmtapi  7.180s
```

All green. Then restored.

**Why it matters.** Port names are the primary key that binds a stored graph to
a rendered config. They are the field with the highest blast radius and the only
one with literally no coverage. The Playwright suite proves the canvas works
against a schema no server ever returns; a user opening the real builder gets
components whose handles are on the wrong side and whose wires disappear on save.

**Direction.** Make the corpus and the frontend fixture *derived*, not authored:
generate `web/tests/fixtures/schema-fixture.ts` from a pinned subset of the real
artifact (the `make generate-corpus` target already exists as a precedent), and
regenerate the goldens with `visual.Render` against `schema.Embedded`. Add a
round-trip test asserting every golden is `Stage1`-valid. Add a guard, modelled
on the excellent `internal/cli/dev_test.go:142-190`, asserting that every corpus
graph's edge ports exist in the real artifact.

---

### CRITICAL-2 — 46% of input ports carry no name, disabling the entire OpenTelemetry surface

**Evidence.** Counted over the committed artifact:

```
total_inputs      103   unnamed (no "prop")   47   → 46%
total_outputs     121   unnamed (no "export")  0
components with ≥1 unnamed input:              46
otel.any ports:     84    otel.metrics/logs/traces ports: 0
```

The cause is visible in `extract.go:80-108`: `portsFromStruct` only inspects
*top-level* fields of the Arguments struct, recursing only into anonymous
untagged embeds. But every otelcol component declares its consumers inside a
tagged block:

```go
// alloy@v1.18.1 internal/component/otelcol/processor/batch/batch.go:41
Output *otelcol.ConsumerArguments `alloy:"output,block"`
// alloy@v1.18.1 internal/component/otelcol/consumer.go:36-40
type ConsumerArguments struct {
    Metrics []Consumer `alloy:"metrics,attr,optional"`
    Logs    []Consumer `alloy:"logs,attr,optional"`
    Traces  []Consumer `alloy:"traces,attr,optional"`
}
```

So the metadata fallback (`extract.go:187-192`) fires and emits a single
name-less `otel.any` input. Downstream, `portHandleId` (`schemaAdapter.ts:29-31`)
gives it the synthetic id `p0`, so the canvas creates an edge with
`to.port = "p0"`, and `render.go:275` looks for `e.To.Port == in.Prop` where
`in.Prop == ""` — no match, edge dropped. If an edge ever *did* match, the writer
would emit a line beginning `  = [...]`, which is a parse error.

Also note `extract.go:34` promises `otel.any` is *"refined per-signal below when
possible"*. There is no such code anywhere in the file; 84/84 otel ports are
`otel.any`, so the wire-type checker in `CanvasPane.tsx:279-281` cannot
distinguish a traces port from a metrics port.

**Why it matters.** Every `otelcol.*` component (the majority of the 46) is
un-wirable in the visual builder, and the three-signal OTel topology that
`otel-three-signals` pretends to test is unreachable in production.

**Direction.** Teach `portsFromStruct` to descend one level into tagged blocks
and synthesise dotted logical names, then teach the renderer to emit a nested
block (`output { metrics = [...] }`) rather than a dotted attribute. The two
changes must land together — the dotted-name half alone is what produced the
unparseable `otel-three-signals` golden.

---

### HIGH-1 — The dev seeder's `demoVisualContents` no longer matches its own graph

**Evidence.** `internal/cli/dev.go:86` was correctly migrated to the real port
model in `8a3c7c5` (edge `e2` is `remote_write.receiver → scrape.forward_to`),
but `demoVisualContents` at `internal/cli/dev.go:92-104` was not. Rendering
`demoVisualGraph` against the real embedded artifact gives:

```
discovery.kubernetes "pods" { role = "pod" }
prometheus.remote_write "demo" {
}
prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.demo.receiver]
}
```

whereas the committed constant says
`prometheus.remote_write "demo" { receiver = [prometheus.scrape.demo.metrics] }`.
The comment at `dev.go:88` admits the constant was *"verified via visual.Render
against the corpus test schema"* — i.e. against the fictional schema.

**Why it matters.** `PipelineService.checkVisualRenderMatch`
(`internal/mgmtapi/rpc_pipeline.go:145-174`) re-renders `wizard_state` with the
**real** registry and compares it to `contents`. Every freshly seeded dev
environment therefore reports the demo pipeline as drifted, and the seeded
`contents` is not what the builder would produce.

**Direction.** Derive the constant at test time instead of hard-coding it: assert
`visual.Render(demoVisualGraph, realSchema).Content == demoVisualContents` in
`dev_test.go`, next to the port-existence guard that already lives there.

---

### HIGH-2 — 33% of overlay `port_display_order` entries point at ports that do not exist, and nothing checks

**Evidence.** Cross-referencing the overlay against the artifact:

```
port_display_order entries: 27
entries with ≥1 dangling port name: 9   (22 dangling names total)
```

Examples: `otelcol.processor.batch` lists six names
(`output.metrics/logs/traces`, `input.metrics/logs/traces`) while the artifact
declares exactly one port, `input`; `otelcol.receiver.otlp` lists three while the
artifact declares zero named ports; `beyla.ebpf` lists `output.traces`, which
does not exist. `Registry.ValidateOverlay` (`internal/schema/registry.go:118-152`)
checks only two things — that overlay component keys exist in the artifact, and
that `discovery_stub` sits on a `discovery.*`/`loki.source.*` key. It never looks
at port names.

**Why it matters.** This is exactly the "overlay silently rots" failure the
overlay guards were meant to prevent, and it is already happening *within one
Alloy version*, before any bump.

**Direction.** Extend `ValidateOverlay` to validate `port_display_order` entries
against the artifact's declared `prop`/`export` names, and to require every port
of a component to appear exactly once when the key is present.

---

### HIGH-3 — `make schema-verify` does not exist; no CI job regenerates or diffs the artifact

**Evidence.**

```
$ grep -rn "schema-verify" .
tools/alloy-schema-gen/README.md:23
docs/visual-builder-design-VB1.md:235, 341, 401
```

Three documents describe it as CI-enforced ("Fails (with diff attached) if they
differ", "Alloy-bump PRs + weekly cron"). There is no such Makefile target and no
such workflow. `.github/workflows/ci.yml` has five jobs — lint, build, guards,
test, web — and none mentions the schema.

Additionally, the artifact is **not diffable as specced** even if the job
existed: `extract.go:213` stamps `"generated_at": time.Now()`. I proved the rest
is reproducible — full clone of `grafana/alloy@v1.18.1`, injected the extractor,
ran it:

```
$ diff <(jq -S 'del(._meta.generated_at)' out.json) \
       <(jq -S 'del(._meta.generated_at)' internal/schema/artifacts/alloy-v1.18.1.json)
(no output — IDENTICAL)
```

So a naive `diff` fails 100% of the time on exactly one line.

**Why it matters.** The only stated protection against "the committed artifact no
longer reflects the pinned Alloy" is imaginary. Anyone can hand-edit
`alloy-v1.18.1.json` and no test or job will notice.

**Direction.** Either implement `schema-verify` (comparing with `generated_at`
excluded, or dropping the field and deriving provenance from the git tag), or
delete the claim from all three documents. Reproducibility is already there — it
just needs a job and one `jq del`.

---

### HIGH-4 — The generator's only real unit tests never run in CI, and `extract.go` is never compiled by this repo

**Evidence.** `.github/workflows/ci.yml:98` runs `go test ./internal/...`.
`tools/alloy-schema-gen/reconcile_test.go` lives outside `./internal/...`, so its
17 specs (the best tests in this subsystem, see below) never execute in CI. They
pass locally: `go test ./tools/... → ok shepherd/tools/alloy-schema-gen`.

`extract.go:1` carries `//go:build ignore`, so `go build ./...`, `go vet ./...`
(both in CI) and `golangci-lint run ./...` all skip it. It is only compiled
during `make schema`, which requires network access and a ~4 GB clone. That is
how the bug fixed in `8a3c7c5` — `run.sh` copying the build constraint through,
making `make schema` fail 100% of the time with *"build constraints exclude all
Go files"* — survived from the initial commit. I reproduced the working path and
the failure mode is still visible in the maintainer's own logs
(`scratchpad/schema.log`).

**Direction.** Change CI to `go test ./...`. For `extract.go`, consider a
compile-only check (a tiny CI step that copies it into a scratch module with
stubbed alloy types, or simply drops the `ignore` tag and moves it to a package
excluded from the main build by directory rather than build tag).

---

### HIGH-5 — Extraction gaps: what the artifact silently omits

All quantified against the committed artifact / the alloy v1.18.1 checkout.

| gap | evidence | impact |
|---|---|---|
| `alloy:",squash"` fields dropped entirely | `walkStruct` requires `len(parts) >= 2` and matches only `attr`/`block` (`extract.go:319-321, 337-356`); a squash tag yields `name=""`, `kind="squash"` → skipped, and its struct is *not* recursed into. 80 squash sites in `internal/component/**`. | `loki.write`'s `endpoint` block is missing `basic_auth`, `authorization`, `oauth2`, `tls_config`, `bearer_token` — every credential field — because they live in `*types.HTTPClientConfig alloy:",squash"` (`alloy/internal/component/loki/write/types.go:30`). Same class of loss on `otelcol.processor.attributes` (`Match otelcol.MatchConfig alloy:",squash"`), which ends up with `attributes: null`. |
| No defaults captured | nothing calls `SetToDefault()` / reads `DefaultArguments`; `default_snippet` is a hard-coded `fmt.Sprintf` (`extract.go:204`). All 184 snippets are exactly `<name> "example" {}`. | 99 components have ≥1 required attribute and 150 required attributes exist fleet-wide; the "default snippet" is invalid config for all of them. The prop editor cannot show a default. |
| No enums | `AttrDef` (`extract.go:113-117`) has no `values` field; 0 attributes in the artifact carry one. | `web/src/visual/types.ts:47` and `InspectorPanel.tsx:72` render a `<select>` when `attr.values?.length` — dead code in production, exercised only by the fixture. |
| No docs | `Doc: ""` for all 184 (`extract.go:161`); overlay supplies component-level prose but nothing per attribute. | No inline help for 1024 attributes. |
| Block "required" not captured | `BlockDef` (`extract.go:119-124`) has `Repeatable` but no `Required`, though the tag distinguishes them. | `output {}` is required on every otelcol component and `endpoint {}` on remote_writes; the UI cannot warn. |
| `maxDepth = 4` truncation | `extract.go:288, 292` | 4 components reach nesting depth ≥4 (`beyla.ebpf`, `otelcol.exporter.loadbalancing`, `otelcol.processor.tail_sampling`, `otelcol.receiver.awscloudwatch`); their deepest blocks are silently cut with no marker. Depth histogram: 56 flat / 81 d1 / 34 d2 / 9 d3 / 2 d4 / 2 d5. |
| Lossy type mapping | `schemaType` (`extract.go:254-283`) collapses to 8 strings. Histogram: 365 string, 198 bool, 168 list, 125 duration, 95 number, 41 secret, 26 map, 6 capsule. | `[]string` and `map[string]string` lose element types; `units.Base2Bytes` (config value `"1MiB"`) becomes `number`; 6 attributes are opaque `capsule`. |
| `Opaque` is dead | 0/184 components have `opaque: true`; `reg.Args` is never nil in v1.18.1. | The `opaque` branch (`extract.go:171, 176`) and `ComponentDef.opaque` in TS are untested dead paths. |

**Direction.** Squash support and block-required are small, high-value additions
to `walkStruct`. Defaults are obtainable by instantiating `reg.Args` and calling
the `alloy.Defaulter` interface where implemented. Docs and enums realistically
belong in the overlay — but then the overlay needs an attribute-level shape and a
guard, which it currently lacks.

---

### MEDIUM-1 — The "edit one version, run one command" bump story is false

I falsified it in three ways:

1. **Bump `ALLOY_VERSION` alone.** `sed -i '' 's/^ALLOY_VERSION=v1.18.1/ALLOY_VERSION=v1.19.0/' deploy/versions.env` then:
   ```
   make check-docker  → check-docker: OK
   go build ./...     → OK
   go test ./internal/schema/... → ok
   ```
   Everything green, `internal/version/alloy_gen.go` still says `alloy-v1.18.1`,
   `ALLOY_IMAGE` still says `v1.18.1`. No guard checks that `ALLOY_IMAGE`'s tag
   equals `ALLOY_VERSION` (`check-docker`, `Makefile:209-223`, only compares
   Dockerfile `ARG` defaults against `versions.env` values), that
   `alloy_gen.go` is current, or that
   `internal/schema/artifacts/alloy-$(ALLOY_VERSION).json` exists.
2. **Regenerate `alloy_gen.go` without the artifact.** `make gen-alloy-version
   ALLOY_VERSION=v1.19.0` → `internal/schema` stays **green** (it hardcodes the
   old version, see MEDIUM-2); only one spec anywhere catches it —
   `internal/mgmtapi/rpc_visual_test.go:84`, "renders a graph over the Connect
   handler (happy path)". One spec is the entire safety net.
3. **`e2e/docker-compose.e2e.yaml:186`** hardcodes `${ALLOY_IMAGE:-grafana/alloy:v1.18.1}`
   and is outside `check-docker`'s three-Dockerfile loop, so its default can
   drift silently.

Also: `GetCurrent` (`internal/mgmtapi/schema.go:70-99`) maps *any* registry error
to `500 internal_error`, so a missing artifact for the pinned version surfaces as
a server error rather than a diagnosable 404/503.

**Direction.** Add a `check-versions` guard: `ALLOY_IMAGE` must end in
`:$(ALLOY_VERSION)`; `internal/version/alloy_gen.go` must equal the output of
`gen-alloy-version` (a `git diff --exit-code` after regenerating); the artifact
file for the pin must exist; extend the Dockerfile loop to the e2e compose file.

---

### MEDIUM-2 — `schema_test.go` hardcodes the version, so a bump silently tests the stale artifact

**Evidence.** `internal/schema/schema_test.go:46, 185, 326` all call
`schema.New(schema.Embedded, "alloy-v1.18.1")` rather than
`version.AlloySchemaVersion`. Confirmed by experiment 2 above: with the pin moved
to v1.19.0 and no v1.19.0 artifact, the entire schema suite passes while
`/api/schema/current` would 500.

**Direction.** Use `version.AlloySchemaVersion` throughout and add one assertion
that `reg.CurrentVersion()` resolves.

---

### MEDIUM-3 — `attributes: null` / `blocks: null` in the artifact crash the inspector

**Evidence.** `extract.go:162-163` initialises both to `[]`, then
`extract.go:174` overwrites with `walkStruct`'s return, which is `nil` when a
struct yields nothing. Result in the committed artifact:

```
attributes == null : 15 components  (otelcol.receiver.otlp, otelcol.processor.attributes,
                                     otelcol.exporter.faro, loki.echo, prometheus.exporter.self, …)
blocks     == null : 56 components
inputs/outputs == null : 0  (explicitly re-normalised at extract.go:196-201)
```

`web/src/visual/types.ts:57-58` declares both as non-optional arrays, and
`web/src/visual/components/InspectorPanel.tsx:63` does `def.attributes.map(...)`
with no guard — selecting any of those 15 nodes throws a `TypeError`. Other call
sites are defensive (`l1.ts:47`, `renderTS.ts:117`, `alloyCompletion.ts:114` all
use `?? []`), which is why this has not been noticed.

**Direction.** Normalise in the extractor the same way `inputs`/`outputs` already
are, and add an artifact-invariant spec asserting all four fields are arrays.

---

### MEDIUM-4 — There is still a second, hand-maintained component list

`internal/schema/registry.go:7` claims *"The text editor's autocomplete now
derives from this same payload — no second component list."* It does not.
`web/src/editor/alloySchema.ts` is 385 lines of hand-curated data covering **16
components** (vs 184 in the artifact), consumed by `alloyCompletion.ts:2` and
guarded by `alloySchema.test.ts`, whose "drift guard" only compares the file
against a `specRequiredComponents` list living in the same file — a closed loop
that can never detect drift from Alloy or from the artifact.

**Direction.** Either wire the editor to the served payload (the data is a strict
superset except for `doc`/`values`) or correct the comment and rename the test so
it stops implying coverage it does not have.

---

### MEDIUM-5 — `/api/schema/current` is served `immutable`

`internal/mgmtapi/schema.go:92` sets `Cache-Control: public, max-age=86400,
immutable` on the **mutable alias**. `immutable` instructs browsers not to
revalidate even on reload. After an Alloy bump, every already-loaded client keeps
the previous schema for up to 24 h while the server renders with the new one —
the exact split-brain that produces "the canvas dropped my edge". The
version-addressed route deserves `immutable`; `current` deserves
`no-cache` + ETag revalidation.

---

### MEDIUM-6 — The schema-migration path has no data

`Registry.Migrations()` (`internal/schema/registry.go:154-161`) reads
`overlay["migrations"]`. `jq 'has("migrations")' overlay.json` → **false**. So
`rpc_visual.go:162`'s migration branch is permanently dead, and the upgrade-check
flow (`internal/visual/upgrade.go`, exercised only against the synthetic
`internal/visual/testdata/schemas/v_old.json` / `v_new.json` with four fake
`test.*` components) has never run on real data.

---

### LOW-1 — `_meta.alloy_version` is `"v1.18.1"`, everything else says `"alloy-v1.18.1"`

`extract.go:211` writes the raw env value. `render.go:238-243` uses it for the
generated-config header, so every rendered file says `schema v1.18.1` while the
graph document it came from pins `schema_version: "alloy-v1.18.1"`. Harmless
today, but it means the header cannot be used to look the schema back up.

### LOW-2 — `run.sh` only works when invoked through `make`

`run.sh:19` does `source versions.env` then a plain assignment
`ALLOY_VERSION="${ALLOY_VERSION:?}"` — never an `export`. Sourced variables are
not inherited by child processes; I verified with a minimal repro:

```
$ ./vtest.sh
(FOO NOT in child env)
```

It works today only because `Makefile:7`'s bare `export` puts it in the
environment. Running `./tools/alloy-schema-gen/run.sh` directly — which
`README.md:13` implies is equivalent — produces an artifact with
`"alloy_version": ""`.

### LOW-3 — `Registry.Get` hands every caller the same mutable map

`registry.go:99-110` caches `merged` and returns the map itself. Two of three
consumers immediately `json.Marshal` it (`rpc_visual.go:61`,
`rpc_pipeline.go:161`) and the handler encodes it, so nothing mutates it today —
but a future consumer that does will corrupt the process-wide cache, and reads
happen outside the mutex once returned.

### LOW-4 — Two specs in `schema_test.go` are tautologies

- `"red run evidence: corrupt components_total fails"` (`schema_test.go:150-178`)
  computes `badTotal := len(components) + 1` and then asserts
  `badTotal != len(components)`. That is `n+1 != n`. It exercises no production
  code path and can never fail.
- `"every default_snippet parses with the Alloy syntax parser (stage 1)"`
  (`schema_test.go:123-148`) does not invoke a parser. Its own body says so:
  *"the full Alloy parser is validated in the integration suite"* — and then
  asserts only non-emptiness and `ContainSubstring(name)`, which is trivially
  true given `extract.go:204` builds the string by formatting the name. The
  repo does have `validate.Stage1` available in-process (I used it), so this
  could be a real check.

---

## What is genuinely good — preserve this

- **The generation is reproducible, and I verified it end to end.** Full clone of
  `grafana/alloy@v1.18.1` (`6012ec4`), extractor injected, run: output is
  byte-identical to the committed 584 KB artifact after deleting one timestamp
  field. Key sorting via `jq -S` (`run.sh:39`) plus `sort.Strings(names)`
  (`extract.go:149`) makes this hold. Very few generated-artifact pipelines
  actually pass this test.
- **Component coverage is complete.** 184/184. I extracted the registered names
  from the alloy source independently and `comm`'d them against the artifact
  keys: zero components in the source and missing from the artifact.
- **`reconcile.go` is the best-engineered piece here.** Small, single-purpose,
  correct on the hard part (editorial fields on surviving entries are never
  touched, `reconcile.go:88-108`), and idempotent in its *formatting* —
  `escapeNonASCII` + Go's sorted map marshal (`reconcile.go:139-183`) means a
  bump produces a minimal diff instead of rewriting 41 KB of unicode escapes.
  Its 17 specs (`reconcile_test.go`) are real: 12 table-driven `categorize`
  cases including the two genuine priority collisions (`discovery.relabel`,
  `otelcol.exporter.*`), 5 merge-behaviour specs with in-memory fixtures, no
  network. This is the model the rest of the subsystem should follow.
- **The overlay is properly curated, not aspirational.** 184/184 entries have a
  category, a non-empty hand-written doc, and an icon; **zero** carry
  `needs_review`. The `needs_review` workflow is real machinery that has simply
  been fully worked through for this version.
- **The generated/hand-maintained split is defensible.** The overlay adds no
  structural data whatsoever (0 entries define `attributes`/`blocks`/`inputs`/
  `outputs`), so a version bump can never be defeated by hand-written structure.
  That is the right boundary.
- **Hermetic builds.** The artifact is committed and `//go:embed`ed
  (`embed.go:11`); `make schema` is deliberately not a build dependency
  (`Makefile:249-253`) and `run.sh:9` says so explicitly. `make build` needs no
  network.
- **The `//go:build ignore` + `sed`-strip injection trick** (`run.sh:35`) is a
  genuinely clever answer to Go's `internal/` visibility rule, and the comment
  above it explains exactly why the strip is needed.
- **`internal/cli/dev_test.go:142-190` is the one test in this subsystem that
  does the right thing**: it loads the *real* embedded artifact via
  `version.AlloySchemaVersion` and asserts every seeded edge references a
  declared port, with a comment explaining precisely the silent-drop failure mode
  it guards. Every corpus and fixture test should look like this.
- **`make check-docker`** is a real, passing guard against `ARG`-default drift,
  with a comment explaining the exact attack (`Makefile:195-200`).
- **Serving is competent**: version-addressed URLs, sha256 ETag with conditional
  GET (`schema.go:82-86`), input sanitisation on `{version}`
  (`schema.go:141-160`), a typed `ErrNotFound`, and graceful 503 when the
  registry fails to initialise instead of a boot panic (`router.go:60-64`).

---

## Test-coverage assessment

### Proven

- The artifact is internally consistent: `_meta.components_total == len(components)`
  (verified — I set it to 999 and the suite went red), every component has a
  valid `stability`, every port type is in the 8-value closed set.
- No orphaned overlay keys (verified — deleting `prometheus.scrape` from the
  artifact turned 3 specs red).
- Every wire type and category carries a `#rrggbb` colour and a label.
- Deep-merge semantics: overlay fields land on artifact components
  (`prometheus.scrape.category == "transform"`), `_meta` survives the merge,
  ETags are stable, unknown versions return `ErrNotFound`.
- `reconcile.go`'s categorisation heuristic and merge behaviour — genuinely,
  including priority collisions and editorial-field preservation.
- The dev seeder's demo *graph* references only real artifact ports.

### Only appears proven

- **Port names.** Zero coverage. Corrupting `prometheus.scrape.inputs[0].prop`
  and `prometheus.remote_write.outputs[0].export` in the committed artifact
  leaves all three relevant packages green.
- **Rendering.** `internal/visual/render_test.go` and the nine corpus goldens
  test `visual.Render` against `corpusSchema()`, a 12-component hand-written
  fiction. 0/9 goldens reproduce against the real schema; 2/9 are not valid
  Alloy at all.
- **The whole frontend visual builder.** 13 files depend on
  `web/tests/fixtures/schema-fixture.ts`, including all 10 Playwright visual
  specs. None ever sees the served payload.
- **`default_snippet` "parses with the Alloy syntax parser"** — the spec title
  claims a parser; the body asserts a substring (LOW-4).
- **"red run evidence: corrupt components_total fails"** — a tautology (LOW-4).
- **The `alloySchema` "drift guard"** — compares a file to a constant in the
  same file (MEDIUM-4).
- **The version-bump procedure** — one incidental mgmtapi spec is the only thing
  that notices a pin/artifact mismatch; the dedicated schema suite does not
  (MEDIUM-1, MEDIUM-2).

### Unverified

- `extract.go` itself: never compiled, vetted, linted or unit-tested by this
  repo (HIGH-4). Its correctness is established only by a human eyeballing a
  584 KB JSON diff.
- Artifact ↔ upstream-Alloy fidelity: nothing compares the artifact to
  `grafana/alloy` at any point in CI (HIGH-3).
- Overlay `port_display_order`, `key_props`, `terminal_ok`, `icon`,
  `discovery_stub` *values* — only `discovery_stub`'s key placement is checked.
- Schema migrations (MEDIUM-6) — no real data has ever exercised the path.
- `opaque: true` components — 0 exist, so both the Go and TS branches are dead.
- Whether any rendered config produced from the *real* schema reaches Alloy
  stage 2/3 successfully: the e2e suite never calls `/api/schema/*`
  (`grep -rn "api/schema" e2e/` → no matches).

---

## Open questions for maintainers

1. **Which port model is the target?** The artifact's Arguments/Exports model is
   faithful to Alloy and renders correct config, but it puts a sink's handle on
   the *right* of the node and a source's on the *left*, which is visually
   backwards. The fixtures encode a dataflow model that reads correctly but is
   not expressible through `render.go`. Deciding this is a prerequisite for
   fixing CRITICAL-1 and CRITICAL-2 — is the canvas going to render logical
   direction (with a mapping layer) or literal argument/export direction?
2. **Was `8a3c7c5` intended as a complete migration?** It fixed the extractor and
   the dev seed but left the corpus, goldens, frontend fixture, overlay
   `port_display_order` and `demoVisualContents` on the old model. Is there a
   follow-up ticket, or did the green test suite make it look finished?
3. **Should `schema-verify` be built or the claim deleted?** Three documents
   describe CI enforcement that does not exist. Reproducibility is already
   proven, so the job is cheap — but someone must own the ~4 GB clone in CI.
4. **Is `generated_at` worth keeping?** It is the only source of
   non-determinism and it blocks the diff-based verification the docs describe.
   Would `alloy_version` + the git tag suffice for provenance?
5. **How far should the extractor go?** Squash fields, nested-block ports and
   defaults are the three gaps that actually bite users (credentials, OTel
   wiring, valid starting snippets). Are those in scope for the extractor, or is
   the plan to hand-author them in the overlay — and if so, what guards the
   overlay's structural additions?
6. **Should `web/src/editor/alloySchema.ts` exist?** 16 hand-maintained
   components alongside 184 generated ones, with a comment in `registry.go`
   already asserting it has been removed.
7. **Who owns the overlay when Alloy adds 30 components?** `reconcile.go` will
   add 30 `needs_review` skeletons with empty docs. The current overlay's 184
   hand-written docs represent real editorial effort — is that sustainable per
   release, and should `needs_review` block a release or only warn?
