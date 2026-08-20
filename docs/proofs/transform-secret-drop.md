# The S3 simulation transform constructs the sandbox config from an allowlist

**Claim proved:** VB-1 §6.4's containment claim — "no real credentials ever enter the sandbox" —
is a property of `internal/simulate.Transform`, not of the validator, not of the NetworkPolicy, and
not of a case list that happens to be complete today.

**The line under test** (`internal/simulate/transform.go`):

```go
r, e = keepProps(&out, req, written, stubbed, refusedNodes(errs))
```

---

## What this replaced, and why the old proof proved less than it looked like

Until this milestone the line under test was:

```go
rewrites = append(rewrites, dropSecretTypedProps(&out, req.Schema)...)
```

— a **type-driven sweep** that deleted every prop whose declared schema type was `secret`. Deleting
it did turn two specs red, so the drill ran and the document said "proved". It was not.

Measured over the shipped artifact (`internal/schema/artifacts/alloy-v1.18.1.json`, pinned as an
assertion in `internal/schema/schema_test.go`):

| | count |
|---|---|
| declared attribute paths | **6482** |
| of those, typed `secret` | **338** |
| credential-NAMED attribute paths | **716** |
| credential-named and **not** typed `secret` | **516** |

So the sweep covered 338 of 6482 paths and said nothing at all about the other 6144. Three real
credentials walked straight through it (security review finding 1), rendered with **zero**
diagnostics and passed real `alloy validate --stability.level=experimental` with exit 0:

- `otelcol.receiver.solace` → `auth.sasl_xauth2.bearer` — declared `string`
- `otelcol.receiver.cloudflare` → `secret` — declared `string`
- `otelcol.processor.resourcedetection` → `openshift.token` — declared `string`

The old post-condition could not catch them either: rule P1 walked the transformed props asserting
`declared != "secret"` — the *same predicate the sweep applied*. A check that re-asks the question
the fix already answered can only catch a bug in the fix, never a gap in the question.

**Type cannot be the allowlist.** Neither can name: `prometheus.scrape.http_headers` is declared
`map`, matches no credential pattern, and carries `Authorization: Bearer …`; conversely
`prometheus.relabel`'s `rule.*.target_label` matches an address pattern and is a label name.

---

## The model now under test

The transformed graph is **constructed, never filtered**. For every surviving node rule K builds a
fresh props map and copies in only:

1. paths rules G and D wrote themselves (a stub's targets, a harness endpoint, a forced TLS
   downgrade), tracked in `writtenPaths`; and
2. authored paths the overlay's `sim_keep` names **for that exact component**, whose value is a
   plain literal of the declared type.

Everything else is absent — not deleted, absent. `$expr` escapes are refused at every path (which
structurally kills `sys.env("EXFIL_URL")`), all `GraphBinding`s are removed (which closes finding
10's channel, where `render.go:541` writes `bind.Prop = bind.Ref.Expr` verbatim with no type check),
and `secret`/`capsule` typed paths are unkeepable however the list is written.

The allowlist unit is the **block-qualified attribute path**. Measured over the shipped overlay: 123
components carry a `sim_keep`, 22 of them as `keep_subtree` on components measured to hold no
credential-, address-, header- or filesystem-shaped attribute at any depth, 30 as an explicit list
totalling 682 path entries, and 71 as an empty list ("nothing this component declares may cross").
Every artifact component resolves to exactly one of five dispositions — `discovery_stub` (35),
`sim_destination` (15), `sim_keep` (108), `sim_secret_source` (14), `sim_unsupported` (6), plus the 6
deliberately-unmappable destinations — and `make schema-verify` fails if one does not. The census is
pinned as an assertion in `internal/schema/schema_test.go`, so those numbers are checked, not
recited.

**A path is not the whole story, and the round-2 review measured what that cost.** See "Round 2: the
keys the path guard could not see" below: for a `map`-typed attribute the effective path continues
into a key the user invents, which no build-time guard over the artifact can read.

---

## Why validation cannot be the safety net

Verified against the real binary, `grafana/alloy:v1.18.1`. The exact config the transform would ship
with rule K removed — capture URL rewritten, `sys.env` password intact — validates cleanly:

```alloy
prometheus.scrape "app" {
  targets = [discovery.relabel.k8s.output]
  forward_to = [prometheus.remote_write.sink.receiver]
  bearer_token = sys.env("TOKEN")
}

discovery.relabel "k8s" {
  targets = [{__address__ = "simulator.example.com:9111"}]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
    basic_auth {
      username = "shepherd"
      password = sys.env("PROM_PASSWORD")
    }
  }
}
```

```
$ docker run --rm -v "$dir:$dir:ro" grafana/alloy:v1.18.1 \
    validate --stability.level=experimental "$dir/leak.alloy"
exit=0
```

Exit 0. `alloy validate` has no opinion about whether a password is real. Only the transform does.

The same run also confirms the correction this milestone had to make to the design text —
`discovery.static`, which §6.4 names as the discovery stub, does not exist in Alloy v1.18.1:

```
$ docker run ... validate --stability.level=experimental "$dir/static.alloy"
Error: .../static.alloy:1:1: cannot find the definition of component name "discovery.static"
Error: validation failed
exit=1
```

---

## The fixture: an exhaustive canary, generated from the artifact

`internal/simulate/transform_keep_test.go` no longer names a handful of credentials. It walks the
shipped artifact, takes **every attribute path whose declared type can hold a string** — 4678 of
them, `string`+`secret`+`capsule`+`duration`+`list`+`map` — plants a unique canary at each, builds a
one-node graph, transforms it, renders it, and asserts:

> the canary appears in the rendered config **if and only if** its path is on that component's keep
> list.

It fails in both directions, and the counts are stated so it cannot pass vacuously: **615** paths
reach the sandbox, **4063** do not. Being generated from the artifact, it grows automatically with
an Alloy bump. (617/4061 before round 2 removed `prometheus.scrape`'s `params` and `metrics_path`
from the keep list.)

What it does **not** measure is whether the keep list is *right*: it compares rule K to the overlay,
so it stays green for any leak the overlay authorises. That is the gap
`internal/simulate/transform_keys_test.go` fills, below.

The three leaks finding 1 proved are also asserted individually, so a regression names the one that
came back.

---

## Red run 1 — rule K deleted

`r, e = keepProps(...)` replaced by `_ = keepProps`. The transform's own post-condition refuses to
return a graph at all, and **two independent mechanisms** fire on the same bug — the structural
allowlist check (P1) and the render-provenance check (P1'), the second computed from the *authored*
graph rather than from rule K's output:

```
$ go test ./internal/simulate/ -count=1

• [FAILED] [0.038 seconds]
Transform: rule K keeps exactly what the overlay allowlists refuses the string-typed credentials
the type-driven sweep let through [It] otelcol.receiver.solace auth.sasl_xauth2.bearer
/Users/.../internal/simulate/transform_keep_test.go:211

  [FAILED] Unexpected error:
      <simulate.TransformErrors | len:2, cap:2>:
      [
          {
              Code: "containment_violated",
              NodeID: "n1",
              Component: "otelcol.receiver.solace",
              Message: "transformed graph carries otelcol.receiver.solace at auth.sasl_xauth2.bearer,
                        which neither the sim_keep allowlist nor the transform's own writes account for",
          },
          {
              Code: "unknown_provenance",
              Message: "transformed render carries a 22-character string literal that is neither a
                        harness value, a transform constant, nor a value authored at an allowlisted path",
          },
      ]
  occurred

Summarizing 21 Failures:
  [FAIL] ... refuses the string-typed credentials the type-driven sweep let through [It] otelcol.receiver.solace auth.sasl_xauth2.bearer
  [FAIL] ... refuses the string-typed credentials the type-driven sweep let through [It] otelcol.receiver.cloudflare secret
  [FAIL] ... removes every binding, whatever prop it names [It] top-level secret-typed prop
  [FAIL] ... removes every binding, whatever prop it names [It] dotted prop the old type lookup could not resolve
  [FAIL] ... removes every binding, whatever prop it names [It] string-typed top-level prop
  [FAIL] ... removes every binding, whatever prop it names [It] a prop that is not credential-shaped at all
  [FAIL] ... [It] refuses an expression at a kept path, so sys.env cannot be smuggled through one
  [FAIL] ... [It] fails closed on a component the overlay gives no keep list
  [FAIL] Transform: the transformed config validates [It] parses: every transformed corpus graph passes stage 1
  [FAIL] Transform: the transformed config validates [It] runs: every transformed corpus graph passes stages 1-2 against the real binary
  [FAIL] Transform: totality and determinism over the shared corpus [It] transforms every corpus graph
  ... (21 total)

Ran 54 of 54 Specs in 5.70 seconds
FAIL! -- 33 Passed | 21 Failed | 0 Pending | 0 Skipped
```

Note what the error message says: it names the *path*, `auth.sasl_xauth2.bearer`, and it says the
allowlist does not account for it. It does not say "declared secret" — because it is not, which is
the whole point.

The exhaustive canary spec is **green** in this run, and that is correct rather than a hole: the
self-check refused every leaking graph before it could be rendered, so nothing reached the text. It
is red in run 2, where the self-check is gone too.

## Red run 2 — rule K AND the self-check deleted

Rule P is the runtime backstop; the canary suite is the test-level one. To show the canary suite is
itself load-bearing rather than shadowed by the self-check, the second drill also replaced
`if e := checkContainment(...)` with `_, _, _ = checkContainment, removed, stubbed`:

```
$ go test ./internal/simulate/ -count=1

• [FAILED] [0.088 seconds]
Transform: rule K keeps exactly what the overlay allowlists [It] plants a canary at every
string-carrying attribute path in the artifact and finds it in the render if and only if the path
is allowlisted
/Users/.../internal/simulate/transform_keep_test.go:121

  [FAILED] 1918 attribute paths reached the sandbox config without being on any keep list
  Expected
      <[]string | len:1918, cap:2560>: [
          "beyla.ebpf open_port",
          "beyla.ebpf executable_name",
          ...
          "beyla.ebpf attributes.kubernetes.kubeconfig_path",
          ...
      ]
  to be empty

• [FAILED] [0.052 seconds]
... [It] otelcol.receiver.solace auth.sasl_xauth2.bearer

  [FAILED] rendered:
  // generated by shepherd visual builder — do not edit by hand ...

  otelcol.receiver.solace "exfil" {
    auth {
      sasl_xauth2 {
        bearer = "CANARY-PROD-CREDENTIAL"
      }
    }
  }

  not to contain substring
      <string>: CANARY-PROD-CREDENTIAL

• [FAILED] [0.039 seconds]
... [It] otelcol.receiver.cloudflare secret

  [FAILED] rendered:
  otelcol.receiver.cloudflare "exfil" {
    secret = "CANARY-PROD-CREDENTIAL"
  }

  not to contain substring
      <string>: CANARY-PROD-CREDENTIAL

Ran 54 of 54 Specs in 11.139 seconds
FAIL! -- 42 Passed | 12 Failed | 0 Pending | 0 Skipped
```

**1918 attribute paths leak** with the construction step removed, and the two named string-typed
credentials render verbatim — exactly the configs the previous design shipped and the previous proof
called clean. This is the run that would have caught CRITICAL-1.

## Red run 3 — the other direction: an allowlist that keeps too little

A deny-by-default transform that quietly becomes deny-EVERYTHING tells the user nothing about their
own pipeline, which is its own critical failure. `scrape_interval` was removed from
`prometheus.scrape`'s keep list in `overlay.json` — one line, nothing else:

```
$ go test ./internal/simulate/ -count=1

• [FAILED]
Transform: the simulated pipeline still shows the user their own pipeline
[It] keeps minimal-scrape's scrape settings

  [FAILED] rendered:
  prometheus.scrape "app" {
    targets = [discovery.relabel.k8s.output]
    forward_to = [prometheus.remote_write.sink.receiver]
    job_name = "app"
  }

  to contain substring
      <string>: scrape_interval = "30s"

Summarizing 4 Failures:
  [FAIL] ... [It] keeps minimal-scrape's scrape settings
  [FAIL] ... [It] keeps kitchen-sink's metric relabel rules and its scrape settings
  [FAIL] ... [It] plants a canary at every string-carrying attribute path ...
  [FAIL] ... [It] refuses an expression at a kept path, so sys.env cannot be smuggled through one

Ran 54 of 54 Specs in 11.493 seconds
FAIL! -- 50 Passed | 4 Failed | 0 Pending | 0 Skipped
```

## Green run — everything restored

```
$ go build ./...
$ go test ./internal/simulate/... ./internal/schema/... -count=1
ok  	shepherd/internal/simulate	13.050s
ok  	shepherd/internal/schema	1.773s

$ golangci-lint run ./...
0 issues.
```

The 54 simulate specs include the stage-2 spec running **real** `alloy validate
--stability.level=experimental` from `grafana/alloy:v1.18.1` over all nine transformed corpus
graphs. That spec no longer self-skips when `alloy` is absent from `PATH` (finding 11): it uses the
docker shim `internal/visual/render_test.go` already had, and `Fail`s unless
`SHEPHERD_SKIP_ALLOY_VALIDATE` is set deliberately. It is the guard that catches an allowlist which
drops an attribute Alloy requires — against the binary, not against our own opinion of the schema.

---

# Round 2: the keys the path guard could not see

The allowlist unit is a path, and `internal/schema`'s name guard checks every segment of it before
the overlay may ship. An independent round-2 review measured where that argument stops.

**A `map`-typed attribute is one segment here and two at runtime.**
`prometheus.remote_write.external_labels` passes the build-time guard — nothing in
`external_labels` is credential-shaped — and what actually reaches the sandbox is
`external_labels.<whatever the user typed>`. Thirteen kept paths in the shipped overlay are declared
`map`. `prometheus.scrape.params` was one of them, which is the case that makes the point sharpest:
`params` is the canonical Prometheus **query-string credential mechanism**, and `policy.go` had
already excluded `http_headers` — the same mechanism in a different transport — for exactly this
reason.

**The `target_set` class had the same hole by a different route.** It forces `__address__` and
`__scheme__`, removes the steering meta labels, and copied *every other label the user named*
verbatim — while `checkProvenance`'s `addTargetSetLiterals` added those same values to the allowed
set, so P1' could not catch what rule K had waved through.

**And `targets` is not always a Prometheus label set.** `prometheus.exporter.blackbox` and
`prometheus.exporter.snmp` declare `targets` as a `list` exactly like `prometheus.scrape` does, and
carry the probe destination in an ordinary `address` key — which forcing `__address__` never
touched.

## Before: five leaks against the shipped overlay

Run from `internal/simprobe/main.go` (a throwaway `main` in-tree, deleted afterwards) against the
pre-fix `transform.go`/`provenance.go` and the pre-fix `overlay.json`, loading the **shipped**
embedded schema, with the same example harness the specs use:

```
$ go run ./internal/simprobe

===== prometheus.exporter.blackbox — probe destination in an ordinary address key =====
prometheus.exporter.blackbox "bb" {
  targets = [{__address__ = "simulator.example.com:9111", __scheme__ = "http", address = "internal-vault.example.net", module = "http_2xx", name = "vault"}]
}

===== prometheus.scrape params — the query-string credential mechanism =====
prometheus.scrape "app" {
  targets = [{__address__ = "simulator.example.com:9111", __scheme__ = "http", job = "app"}]
  params = {api_key = ["CANARY-USER-KEY-CREDENTIAL"]}
  metrics_path = "/metrics?token=CANARY-USER-KEY-CREDENTIAL"
}

===== prometheus.remote_write external_labels — a credential at a user-chosen key =====
prometheus.remote_write "rw" {
  external_labels = {api_key = "CANARY-USER-KEY-CREDENTIAL", cluster = "eu-1"}
  endpoint {
    url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
  }
}

===== prometheus.scrape targets — a credential parked in a user-named target label =====
prometheus.scrape "app" {
  targets = [{__address__ = "simulator.example.com:9111", __scheme__ = "http", api_key = "CANARY-USER-KEY-CREDENTIAL", job = "app"}]
}

===== node label — the renderer writes it into the block header =====
prometheus.scrape "ghp_0123456789abcdef" {
}
```

Every one of those transformed cleanly, with no error and no diagnostic.

## After: the same five graphs

Same probe, same harness, current tree:

```
$ go run ./internal/simprobe

===== prometheus.exporter.blackbox — probe destination in an ordinary address key =====
REFUSED: cannot simulate prometheus.exporter.blackbox — probes a destination the user names, and its
target list is not a Prometheus label set: the probe address rides in an ordinary "address" key,
which the target_set class does not touch (it forces __address__ and __scheme__ only). …

===== prometheus.scrape params — the query-string credential mechanism =====
prometheus.scrape "app" {
  targets = [{__address__ = "simulator.example.com:9111", __scheme__ = "http", job = "app"}]
}

===== prometheus.remote_write external_labels — a credential at a user-chosen key =====
prometheus.remote_write "rw" {
  external_labels = {cluster = "eu-1"}
  endpoint {
    url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
  }
}

===== prometheus.scrape targets — a credential parked in a user-named target label =====
prometheus.scrape "app" {
  targets = [{__address__ = "simulator.example.com:9111", __scheme__ = "http", job = "app"}]
}

===== node label — the renderer writes it into the block header =====
REFUSED: node label has a credential shape (a GitHub personal access token) and the renderer writes
it into the block header; rename the node
```

`cluster = "eu-1"` surviving in the third case is load-bearing: the guard **narrows an allowlist**,
it is not one. A key it does not recognise still only survives because the path it sits under was
allowlisted.

## What changed

| Change | Where |
|---|---|
| `schema.IsCredentialName` exported, so one regex serves both guards | `internal/schema/simpolicy.go` |
| rule K's `constrainKeys` re-runs it over every user-chosen key inside a kept value, at any depth | `internal/simulate/transform.go` |
| the `target_set` class drops a user-named label whose name is credential-shaped | `internal/simulate/transform.go` |
| P1'' refuses a credential-**shaped** node label, which the renderer writes into the block header | `internal/simulate/transform.go` |
| `addLiteral` / `addTargetSetLiterals` mirror the drop, so P1' stops laundering those values | `internal/simulate/provenance.go` |
| `prometheus.scrape` no longer keeps `params` or `metrics_path` | `internal/schema/artifacts/overlay.json` |
| `prometheus.exporter.blackbox` / `.snmp` moved from `sim_keep` to `sim_unsupported` | `internal/schema/artifacts/overlay.json` |

## The spec that can fail when the keep list itself is wrong

`internal/simulate/transform_keep_test.go` measures rule K *against the overlay*, with a hard-coded
count — so it is green for any leak the overlay authorises, which is how all five of the above stayed
green through round 1. `internal/simulate/transform_keys_test.go` asks the other question: it
enumerates the kept `map` paths and the `target_set` paths **from the shipped overlay** and plants
the credential at a key the *user* chose, so it goes red when the keep list gains an unguarded key
space rather than when rule K disagrees with the list.

Its two sweeps collect three outcomes, not one: a **leak** (the credential rendered), a **refusal**
(the run failed instead of dropping one entry — still a defect, because `external_labels = {"api_key"
= "…"}` is an ordinary settings map), and an **undisclosed** drop (§6.5: the user must be told).

## Red run 4 — rule K's `constrainKeys` deleted

`out[a.Name] = raw` restored in `build()`'s default branch, nothing else touched. The credential does
not ship — **P1' catches it instead**, which is the independence the two checks are built for:

```
$ go test ./internal/simulate/ -count=1 -args -ginkgo.no-color \
    -ginkgo.focus="credential-named key in every kept map path|still keeps an ordinary user-chosen key"

• [FAILED] [0.054 seconds]
Transform: a credential at a user-chosen key does not reach the sandbox [It] plants a
credential-named key in every kept map path and finds it in no render

  [FAILED] 12 kept map paths made the whole run fail rather than dropping one entry
  Expected
      <[]string | len:12, cap:16>: [
          "loki.process stage.geoip.*.custom_lookups: transformed render carries a 26-character string literal that is neither a harness value, a transform constant, nor a value authored at an allowlisted path",
          "loki.process stage.json.*.expressions: …",
          "loki.process stage.labels.*.values: …",
          "loki.process stage.logfmt.*.mapping: …",
          "loki.process stage.static_labels.*.values: …",
          "loki.process stage.structured_metadata.*.values: …",
          "loki.write external_labels: …",
          "otelcol.exporter.splunkhec splunk.telemetry.override_metrics_names: …",
          "otelcol.exporter.splunkhec splunk.telemetry.extra_attributes: …",
          "prometheus.remote_write external_labels: …",
          "prometheus.write.queue endpoint.*.external_labels: …",
          "pyroscope.write external_labels: …",
      ]
  to be empty

• [FAILED] [0.040 seconds]
… [It] still keeps an ordinary user-chosen key, so the guard narrows an allowlist rather than being one

  [FAILED] Unexpected error:
      <simulate.TransformErrors | len:1, cap:1>:
      [ { Code: "unknown_provenance", Message: "transformed render carries a 26-character string
          literal that is neither a harness value, a transform constant, nor a value authored at an
          allowlisted path" } ]
  occurred

Ran 2 of 89 Specs in 2.824 seconds
FAIL! -- 0 Passed | 2 Failed | 0 Pending | 87 Skipped
```

## Red run 5 — `constrainKeys` AND its provenance mirror deleted

Second gate removed too (`addLiteral` stops skipping credential-named keys). Now the credential
actually ships:

```
$ go test ./internal/simulate/ -count=1 -args -ginkgo.no-color \
    -ginkgo.focus="credential-named key in every kept map path|still keeps an ordinary user-chosen key"

  [FAILED] 12 kept map paths carried a credential at a user-chosen key into the sandbox config
  Expected
      <[]string | len:12, cap:16>: [
          "loki.process stage.geoip.*.custom_lookups",
          "loki.process stage.json.*.expressions",
          "loki.process stage.labels.*.values",
          "loki.process stage.logfmt.*.mapping",
          "loki.process stage.static_labels.*.values",
          "loki.process stage.structured_metadata.*.values",
          "loki.write external_labels",
          "otelcol.exporter.splunkhec splunk.telemetry.override_metrics_names",
          "otelcol.exporter.splunkhec splunk.telemetry.extra_attributes",
          "prometheus.remote_write external_labels",
          "prometheus.write.queue endpoint.*.external_labels",
          "pyroscope.write external_labels",
      ]
  to be empty

… [It] still keeps an ordinary user-chosen key, …

  [FAILED] rendered:
  prometheus.remote_write "rw" {
    external_labels = {api_key = "CANARY-USER-KEY-CREDENTIAL", cluster = "eu-1"}
    endpoint {
      url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
    }
  }
  not to contain substring
      <string>: CANARY-USER-KEY-CREDENTIAL

Ran 2 of 89 Specs in … FAIL! -- 0 Passed | 2 Failed | 0 Pending | 87 Skipped
```

## Red run 6 — the `target_set` label-name guard deleted

Same two-stage result on the class. With only the class guard removed, P1' refuses all twelve:

```
$ go test ./internal/simulate/ -count=1 -args -ginkgo.no-color \
    -ginkgo.focus="credential-named label in every target_set path"

  [FAILED] 12 target sets made the whole run fail rather than dropping one label
  Expected
      <[]string | len:12, cap:16>: [
          "database_observability.mysql targets: transformed render carries a 26-character string literal that is neither a harness value, …",
          "database_observability.postgres targets: …",
          "database_observability.sql_server targets: …",
          "discovery.relabel targets: …",
          "loki.enrich targets: …",
          "otelcol.processor.discovery targets: …",
          "prometheus.enrich targets: …",
          "prometheus.scrape targets: …",
          "pyroscope.ebpf targets: …",
          "pyroscope.enrich targets: …",
          "pyroscope.java targets: …",
          "pyroscope.scrape targets: …",
      ]
  to be empty

Ran 1 of 89 Specs in 2.666 seconds
FAIL! -- 0 Passed | 1 Failed | 0 Pending | 88 Skipped
```

With `addTargetSetLiterals`'s mirror removed as well, the same twelve become real leaks:

```
  [FAILED] 12 target sets carried a credential in a user-named label into the sandbox config
  Expected
      <[]string | len:12, cap:16>: [
          "database_observability.mysql targets",
          … (the same twelve, now rendered) …
      ]
  to be empty

Ran 1 of 89 Specs in … FAIL! -- 0 Passed | 1 Failed | 0 Pending | 88 Skipped
```

## Red run 7 — the P1'' node-label check deleted

```
$ go test ./internal/simulate/ -count=1 -args -ginkgo.no-color \
    -ginkgo.focus="credential-shaped node label"

• [FAILED] [0.067 seconds]
… [It] refuses a credential-shaped node label, which the renderer writes into the block header

  [FAILED] Expected an error to have occurred.  Got:
      <nil>: nil

Ran 1 of 89 Specs in 2.899 seconds
FAIL! -- 0 Passed | 1 Failed | 0 Pending | 88 Skipped
```

## Red run 8 — the overlay changes reverted

`params` and `metrics_path` put back on `prometheus.scrape`'s keep list, and `sim_keep` put back on
both probe exporters, with `transform.go` untouched:

```
$ go test ./internal/simulate/ -count=1 -args -ginkgo.no-color \
    -ginkgo.focus="no longer keeps prometheus.scrape params|probe exporter whose destination"

• [FAILED] … [It] no longer keeps prometheus.scrape params or metrics_path at all
  [FAILED] params is the canonical Prometheus query-string credential mechanism
  Expected
      <bool>: true
  to be false

• [FAILED] … refuses a probe exporter whose destination is an ordinary address key [It] prometheus.exporter.blackbox
  [FAILED] Expected an error to have occurred.  Got:
      <nil>: nil

• [FAILED] … refuses a probe exporter whose destination is an ordinary address key [It] prometheus.exporter.snmp
  [FAILED] Expected an error to have occurred.  Got:
      <nil>: nil

Ran 3 of 89 Specs in 2.907 seconds
FAIL! -- 0 Passed | 3 Failed | 0 Pending | 86 Skipped
```

## Green run — everything restored

```
$ go build ./...
$ go test ./internal/... 2>&1 | grep -v "no test files"
ok  	shepherd/internal/ado	(cached)
ok  	shepherd/internal/agentapi	(cached)
ok  	shepherd/internal/auth	(cached)
ok  	shepherd/internal/cli	0.575s
ok  	shepherd/internal/config	(cached)
ok  	shepherd/internal/crypto	(cached)
ok  	shepherd/internal/gitrepo	(cached)
ok  	shepherd/internal/gitsync	(cached)
ok  	shepherd/internal/graph	(cached)
ok  	shepherd/internal/merge	(cached)
ok  	shepherd/internal/mgmtapi	10.462s
ok  	shepherd/internal/netshape	1.938s
ok  	shepherd/internal/schema	3.839s
ok  	shepherd/internal/server	1.465s
ok  	shepherd/internal/simsvc	3.508s
ok  	shepherd/internal/simulate	16.892s
ok  	shepherd/internal/store	(cached)
ok  	shepherd/internal/validate	(cached)
ok  	shepherd/internal/visual	12.788s
ok  	shepherd/internal/wizard/appobservability	(cached)

$ golangci-lint run ./...
0 issues.
```

## The residual, stated rather than implied

- **A credential typed into a free-text value at a kept path still reaches the sandbox.** The keep
  list allows `job_name`; nothing can stop a user putting a password in it. The KEY name is the only
  signal of deliberateness there is, and the guards above are that signal used to its limit.
- **A credential-named key the regex does not recognise survives.** That is the false-negative side
  of a heuristic used as a narrowing rather than as an allowlist: the value is where the keep list
  already put it, and it costs nothing that the guard did not also catch it.
- **A node label that is a credential but not credential-*shaped* survives.** `sanitizeLabel`
  lower-cases and strips everything outside `[a-z0-9_]`, which destroys base64, case-dependent JWTs
  and every punctuation-bearing key format, and leaves a lowercase hex secret intact. The label is
  the user's own name for their own node, is never assembled from another field, and no allowlist
  can be written over free text. Accepted, disclosed here, and refused for the shapes P4 knows.
- **The measured false positive is `__meta_puppetdb_certname`** (it matches `cert`) in a
  hand-written target set. It costs one label in one relabel simulation, it is disclosed as a
  `prop_dropped` entry, and a user reaching puppetdb the ordinary way — `discovery.puppetdb` — never
  meets it, because rule G builds that node's targets and rule K skips stubbed nodes.

---

## Build-time guards: the Alloy bump cannot open a hole quietly

`make schema-verify` now runs the overlay guards as well as the artifact diff. Each red run below
was executed against a doctored copy of the artifact or overlay, so the failure path is exercised
rather than asserted to be absent (`internal/schema/schema_test.go`):

| Red run | Injected | Result |
|---|---|---|
| new component | `otelcol.processor.futurething` added to the artifact | `component "otelcol.processor.futurething" has no S3 disposition` |
| new attribute inside a subtree keep | `password` added inside `loki.process`'s stage tree | `sim_keep on "loki.process" keeps "…password", whose segment "password" is credential-shaped` |
| credential kept without acknowledgement | `bearer_token_file` added to `prometheus.scrape`'s keep list | `…keeps "bearer_token_file", whose segment "bearer_token_file" is credential-shaped` |
| keep path that does not resolve | `endpoint.*.queue_config.no_such_attribute` | `names path … which the artifact does not declare` |
| keep path written non-canonically | `endpoint.queue_config.capacity` (missing the `*`) | `canonical form is "endpoint.*.queue_config.capacity"` |
| acknowledgement with no reason | `{"path": "rule.*.target_label"}` | `…with no reason` |

The name heuristics are used **only as a guard on the allowlist, never as the allowlist**. A false
negative costs nothing — an unlisted path is absent either way — and a false positive is resolved
once, explicitly, in `sim_keep_acknowledged` with a written reason. The shipped overlay carries
seven, each of which is a genuine false positive of the address heuristic: five `rule.*.target_label`
paths (a relabel destination *label* name), `otelcol.exporter.prometheus.include_target_info` (a
declared `bool`) and `prometheus.scrape.target_limit` (a declared `number`).

---

## Scope note

Every check below is a **credential** assertion. None of them is a reachability assertion, and the
one that used to claim to be — rule P5, `address_not_harness` — is deleted: static analysis of the
rendered text cannot bound where a relabel rule will steer a scrape, because the address is computed
at runtime from label data. Reachability is denied by the network the sandbox runs on, proven by
execution in `e2e/sandbox_egress_test.go`. See `docs/proofs/simulator-containment.md`.

They are deliberately split between structure, parsed AST and raw text so a bug that defeats one is
unlikely to defeat the others:

| Check | Where | What it asserts |
|---|---|---|
| P1 | inside `Transform` | every surviving props path is on the keep list or was written by rules G/D — re-derived from the policy, not from rule K's output |
| P1 (bindings) | inside `Transform` | no `GraphBinding` survives at all |
| P1'' | `checkContainment` | no node label carries a credential shape — the one authored string the renderer writes without passing rule K, and one the parsed-AST checks cannot see because a block label is not a string literal |
| P1' | `checkProvenance` | every string literal in the **parsed render** is a harness value, a transform constant, or a value authored at an allowlisted path — computed from the AUTHORED graph, so it is independent of rule K, and skipping credential-named map keys is what keeps it independent of `constrainKeys` too |
| P2 | `checkContainment` | no removed secret source's `<component>.<label>.` token appears in the render |
| P2' | `checkContainment` | no `sim_secret_source` component name appears in the render at all |
| P3 | `checkContainment` | the render produces zero `secret_by_value` diagnostics |
| P4 | `checkProvenance` | no surviving literal starts with a high-confidence credential shape (`-----BEGIN `, `eyJ`, `AKIA`, `ghp_`, `xox[abprs]-`, `Bearer `) |

Rule K itself carries two guards that are not post-conditions but constructions: the `target_set`
value class, which forces `__address__` and `__scheme__` from the harness whatever literal the user
typed, and `constrainKeys`, which re-runs `schema.IsCredentialName` over the user-chosen keys the
build-time guard cannot read.

### What this proof does NOT cover

- **Reachability.** Nothing here bounds where the sandbox can connect, and no claim in this document
  should be read that way. A `discovery.relabel` rule with `target_label = "__address__"` and a
  `replacement` assembled from regex captures retargets the scrape at runtime with no host-shaped
  token anywhere in the rendered text — proven end to end against real Alloy v1.18.1 by the round-2
  attack panel, and asserted as a deliberate negative in
  `internal/simulate/transform_address_test.go` ("Transform: a runtime retarget is NOT contained by
  the transform"). The `target_set` class closes the LITERAL address case and nothing more. The
  network denies the reach.
- **Egress denial.** Docker's `internal: true` sim network is the reachability control.
  `e2e/sandbox_egress_test.go` verifies its observable effect by execution — see
  `docs/proofs/simulator-containment.md` for what has and has not been run against a live stack.
- **Kubernetes.** Finding H5's artifacts now exist in the chart —
  `deploy/helm/shepherd/templates/{deployment,service,serviceaccount,networkpolicy}-simulator.yaml`,
  with a default-deny egress NetworkPolicy and `automountServiceAccountToken: false` on both the
  ServiceAccount and the Pod, covered by `deploy/helm/chart_test.go` (`ok
  shepherd/deploy/helm 1.359s`). Their observable effect on a live cluster has not been verified by
  execution the way `e2e/sandbox_egress_test.go` verifies the compose network; a rendered-template
  assertion is not a probe.
- **A credential the user deliberately types into a kept free-text value.** See "The residual,
  stated rather than implied" above.
- **Availability of unstubbed sources (finding 9).** The Sources components with no
  `discovery_stub` carry an empty keep list, so every authored setting — including
  `prometheus.exporter.mysql`'s `data_source_name` — is dropped, and `dropDeadWeight` removes the
  husk once nothing consumes it. The two whose target list carried a probe destination in an
  ordinary `address` key (`prometheus.exporter.blackbox`, `prometheus.exporter.snmp`) are
  `sim_unsupported` and fail the run closed.

The feature stays **disabled by default** until the Kubernetes and egress items above are closed.
