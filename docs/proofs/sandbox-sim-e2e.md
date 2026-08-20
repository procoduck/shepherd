# The S3 sandbox delivers: a real run, end to end, against real Alloy

**Scope.** VB-1 §6.4 / §7.8 item 2 / §11 item 9 / §13 milestone 7. Every other proof in this
directory is about what the sandbox REFUSES. This one is about what it DELIVERS: a user's graph
submitted through the real run API, executed by real `grafana/alloy:v1.18.1` inside the
`shepherd-simulator` container, coming back with captured series, component health, a rewrite
disclosure and Alloy's own stderr.

**Everything below is transcript from a live stack**, not inspection:

```
$ docker compose -f e2e/docker-compose.e2e.yaml --profile sim ps --format '{{.Service}}\t{{.Status}}'
alloy           Up 5 minutes
egress-canary   Up 5 minutes (healthy)
gitea           Up 5 minutes (healthy)
mockmsft        Up 5 minutes (healthy)
oidc            Up 5 minutes (healthy)
postgres        Up 5 minutes (healthy)
shepherd        Up 2 seconds (healthy)
simulator       Up 5 minutes (healthy)
```

Brought up with `SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml
--profile sim up -d --wait`. `SHEPHERD_SIM_ENABLED` is set on the command line for the run only —
the committed default in `e2e/docker-compose.e2e.yaml` is still `false`, and the feature remains
disabled by default in every committed file.

Runs were driven through the same REST surface the UI uses: local-admin login →
`POST /api/admin/orgs` → `POST /api/orgs/{org}/simulate/runs` → poll
`GET /api/orgs/{org}/simulate/runs/{id}` to a terminal state.

---

## 1. Finding M13: the sandbox could not run the graph the design doc names — fixed

The first real run of `minimal-scrape` (the corpus graph §7.8 names: `discovery.kubernetes` →
`prometheus.scrape` → `prometheus.remote_write`) **failed**, and not for a sandbox reason:

```
POST /api/orgs/4a653e97-.../simulate/runs -> HTTP 200
run: 26f047a3-97b7-4578-8bb8-475bbafe1b65
  status: queued
  status: running
  status: failed
status        : failed
error_code    : 'gate_failed'
error_message : 'alloy_start_failed: alloy_start_failed: alloy exited during the run: exit status 1'
captured_series: 0
stderr_tail   : 5253 bytes
  | Error: /tmp/shepherd-sim/run/a210ec.../config.alloy:8:14: discovery.relabel.k8s.output
  |        target::ConvertFrom: conversion from '[]discovery.Target' is not supported
  |  7 | prometheus.scrape "app" {
  |  8 |   targets = [discovery.relabel.k8s.output]
  |    |              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  |  9 |   forward_to = [prometheus.remote_write.sink.receiver]
```

**Root cause, isolated against the bare Alloy image with no Shepherd involved.** Two hand-written
configs differing only in the brackets:

```
$ docker run --rm -v $D:/c:ro grafana/alloy:v1.18.1 validate /c/bracket.alloy   # exit 0
$ docker run --rm -v $D:/c:ro grafana/alloy:v1.18.1 run      /c/bracket.alloy
level=error msg="failed to evaluate config" node=prometheus.scrape.app
  err="decoding configuration: /c/bracket.alloy:10:17: discovery.relabel.k8s.output
       target::ConvertFrom: conversion from '[]discovery.Target' is not supported"
Error: could not perform the initial load successfully

$ docker run --rm -v $D:/c:ro grafana/alloy:v1.18.1 run      /c/bare.alloy      # runs cleanly
```

`alloy validate` **cannot see this bug**. Only `alloy run` evaluates the dataflow graph. That is
why it survived: the corpus golden tests, the transform's `Stages12` gate and the ordinary
fleet-delivery path all stop at `validate`, so the first thing in the repo ever to execute a
rendered config was this sandbox — and it found a defect that would have hit **real fleet agents**
through the ordinary pipeline path with no sandbox involved at all.

The defect was in `internal/visual/render.go`, which bracket-wrapped every wired input because all
193 input ports in the shipped artifact carry `cardinality: "list"` (verified against
`internal/schema/artifacts/alloy-v1.18.1.json`, not quoted from the design doc):

```go
value := texts[0]
if in.Cardinality == "list" {
    value = "[" + strings.Join(texts, ", ") + "]"
}
```

Correct for the receiver-shaped port types (`otel.*`, `loki.logs`, `prom.metrics`,
`pyroscope.profiles` — each export is one receiver and the argument wants a list). Wrong for the
one port type whose Alloy value is ALREADY a list: `targets`, which is 17 of those 193 input
ports, on 17 components.

**Fix** — `refValue` in `internal/visual/render.go`: bare reference for a single `targets` wire,
`array.concat(...)` for several. Both spellings verified with `alloy run`, not just `validate`:

| rendering | `alloy validate` | `alloy run` |
|---|---|---|
| `targets = [discovery.relabel.k8s.output]` (old) | exit 0 | **fails**: `ConvertFrom` |
| `targets = discovery.relabel.k8s.output` (new, 1 wire) | exit 0 | runs cleanly |
| `targets = array.concat(a.output, b.output)` (new, n wires) | exit 0 | runs cleanly, no deprecation warning |

`make generate-corpus` regenerated the goldens; the diff is exactly the `targets` lines and
nothing else — 7 corpus entries, 11 lines, no other port type touched:

```
-  targets = [discovery.kubernetes.k8s.targets]
+  targets = discovery.kubernetes.k8s.targets
-  targets = [discovery.relabel.first.output, discovery.relabel.second.output]
+  targets = array.concat(discovery.relabel.first.output, discovery.relabel.second.output)
```

Every committed golden previously described a config real Alloy refuses to run.

**Red-run proof of the new specs** (removing the `targetsPortType` branch from `refValue`, then
running the suite):

```
$ go test ./internal/visual/
  [FAIL] ParseAlloy with the shipped schema [It] imports the legacy bracket-wrapped targets wire
         and re-renders it runnable
  FAIL! -- 56 Passed | 15 Failed
$ # restore
$ go test ./internal/visual/
ok  	shepherd/internal/visual	7.486s
```

The 15 include every corpus golden. The named spec is new: it pins that a config generated
*before* this fix still imports with its wire intact and re-renders in the runnable spelling —
i.e. that re-import is the migration path for configs already in the wild.

---

## 2. The complete run: it delivers

Same graph, after the fix (`scrape_interval = "5s"`, `scrape_timeout = "2s"` — see §5 for why the
timing is load-bearing), `duration_seconds: 18`:

```
POST /api/orgs/9e719f20-.../simulate/runs -> HTTP 200
run: 72c1c011-b153-492b-9bbd-47cbb796204e
  status: queued  (t+0s)
  status: running (t+2s)
  status: completed (t+20s)

status        : completed
error_code    : ''
error_message : ''

rewrites      : [
  { "node_id": "n1", "node_label": "k8s", "component": "discovery.kubernetes",
    "kind": "discovery_stubbed",
    "detail": "replaced by discovery.relabel emitting the \"k8s-pod-targets\" fixture;
               authored settings, bindings and inbound wires were dropped" },
  { "node_id": "n3", "node_label": "sink", "component": "prometheus.remote_write",
    "kind": "destination_endpoint",
    "detail": "endpoint re-pointed at the simulator's capture receiver" }
]

component_health:
  n3/sink prometheus.remote_write: healthy — started component
  n1/k8s  discovery.kubernetes:    healthy — started component
  n2/app  prometheus.scrape:       healthy — started component

captured_series: 21
  scrape_duration_seconds                      {instance=simulator:9111, job=app}  samples=2
  scrape_samples_post_metric_relabeling         {instance=simulator:9111, job=app}  samples=2
  scrape_samples_scraped                        {instance=simulator:9111, job=app}  samples=2
  scrape_series_added                           {instance=simulator:9111, job=app}  samples=2
  shepherd_sim_queue_depth                      {instance=simulator:9111, job=app}  samples=2
  shepherd_sim_request_duration_seconds_bucket  {le=+Inf .. 1.0, 9 buckets}          samples=2
  shepherd_sim_request_duration_seconds_count   {instance=simulator:9111, job=app}  samples=2
  shepherd_sim_request_duration_seconds_sum     {instance=simulator:9111, job=app}  samples=2
  shepherd_sim_requests_total  {job=app, method=GET,  path=/api/collectors}          samples=2
  shepherd_sim_requests_total  {job=app, method=GET,  path=/api/health}              samples=2
  shepherd_sim_requests_total  {job=app, method=GET,  path=/api/pipelines}           samples=2
  shepherd_sim_requests_total  {job=app, method=POST, path=/api/pipelines}           samples=2
  up                                            {instance=simulator:9111, job=app}  samples=2

stderr_tail   : 7856 bytes, 48 lines, contains "{^_^} Alloy is running"
  | ts=...Z level=info msg="Replaying WAL" component_id=prometheus.remote_write.sink
  |    url=http://simulator:9110/capture/prometheus/api/v1/write
  | ts=...Z level=info msg="scrape manager stopped" component_id=prometheus.scrape.app
  | ts=...Z level=info msg="Remote storage stopped." ...
  | ts=...Z level=info msg="node exited without error" node=discovery.relabel.k8s

fidelity_note : Sandbox run (tier S3): destinations point at this harness rather than your real
                backends, and discovery/log sources are replaced by synthetic data. ...
```

Reading the evidence:

- **`shepherd_sim_requests_total` is the synthetic exporter's own counter**, scraped out of the
  harness at `simulator:9111` — the sandbox really executed a scrape and really delivered it
  through `remote_write` to the capture receiver.
- **`job="app"`** comes from the authored `job_name` prop: the user's own setting survived the
  transform and reached running Alloy.
- **`instance="simulator:9111"`** is emitted by nobody — not the exporter (it exposes only
  `path`/`method`), not the authored graph. It is Prometheus's own scrape-time relabelling of
  `__address__`, i.e. evidence that the relabel machinery ran, which is the whole point of
  simulating relabel rules.
- **`sample_count=2`** — two scrapes at 5s inside an 18s run, flushed through the WAL.
- **All three authored nodes appear in health**, including the stubbed `discovery.kubernetes`.

### Failures reach the user as a run, never as a 500

A genuinely broken graph — `scrape_interval = "5s"` with Alloy's default 10s `scrape_timeout` —
is refused by Alloy at load. The API still answers `HTTP 200` on submit and the run row carries
the diagnosis:

```
POST .../simulate/runs -> HTTP 200
status        : failed
error_code    : 'gate_failed'
error_message : 'alloy_start_failed: alloy_start_failed: alloy exited during the run: exit status 1'
stderr_tail   : | Error: .../config.alloy:7:1: Failed to build component: decoding configuration:
                |   scrape_timeout (10s) greater than scrape_interval (5s) for scrape config
                |   with job name "app"
```

That is the sandbox doing its job: the same config would have failed on a real fleet agent, and
the user finds out here instead, with Alloy's own words.

---

## 3. The kill probe: break the destination rewrite, captures go to zero

**The line under test** — `rewriteDestinations` (`internal/simulate/transform.go:590`), disabled
by inserting `return nil, nil` as the function's first statement, rebuilding `shepherd:local` and
restarting only the `shepherd` container (`up -d --no-deps shepherd`; the simulator was untouched,
so this exercises Shepherd's transform, not the harness).

Same `minimal-scrape` graph, unchanged, both halves.

### RED — rewrite disabled

```
run: 1cb48be0-5a94-48fe-ad4f-78be400eddf6
status        : completed
rewrites      : [
  { "node_id": "n1", "kind": "discovery_stubbed", ... },
  { "node_id": "n3", "component": "prometheus.remote_write", "kind": "prop_dropped",
    "detail": "not on this component's sim_keep allowlist, so it cannot be shown to be
               free of credentials or endpoints" }
]
component_health:
  n3/sink prometheus.remote_write: healthy — started component
  n1/k8s  discovery.kubernetes:    healthy — started component
  n2/app  prometheus.scrape:       healthy — started component
captured_series: 0
```

**Zero captured series** — the assertion that matters. No `destination_endpoint` rewrite is
disclosed, because rule D never ran.

Two things this transcript proves beyond the kill:

1. **Containment held anyway, through a different mechanism than the last time this probe was
   run.** With rule D gone, the authored `endpoint` block is no longer on anything's keep list, so
   deny-by-default rule K drops it (`prop_dropped`) — the user's `https://prometheus.example.com/`
   never reaches the rendered config at all. (The previous transcript of this probe, before the
   round-2 keep-list inversion, showed the harness's own `CheckEndpoints` allowlist refusing the
   run with `endpoint_not_allowed` instead. Either way, two independent gates, and the network
   under both.)
2. **A "completed" status is not evidence of delivery.** This run completed, with three healthy
   components, and delivered nothing. It is the sharpest possible statement of VB-1 §6.4's rule
   that the UI and the tests must key on captured series — never on status, never on health.

### GREEN — restored

`rewriteDestinations` reverted (`grep -c "KILL PROBE" internal/simulate/transform.go` → `0`),
image rebuilt, container restarted, same graph resubmitted:

```
run: 72c1c011-b153-492b-9bbd-47cbb796204e
status         : completed
rewrites       : discovery_stubbed, destination_endpoint
captured_series: 21   (including shepherd_sim_requests_total)
component_health: 3 × healthy
```

`go build ./...` green after the revert; the file is byte-identical to its pre-probe state.

---

## 4. The failure path: an unhealthy component, honestly reported

VB-1 §6.4 requires that a run whose component goes unhealthy still **completes** with that state
in the health tab, rather than failing the request. Two halves, and the second is a negative
result worth stating plainly.

**The reachable half — a graph that captures nothing still completes with a populated health tab.**
`prometheus.scrape` with an empty literal target list, forwarding to a rewritten
`prometheus.remote_write`:

```
status         : completed
error_code     : ''
rewrites       : [ { "kind": "destination_endpoint", ... } ]
component_health:
  n2/sink  prometheus.remote_write: healthy — started component
  n1/empty prometheus.scrape:       healthy — started component
captured_series: 0
```

HTTP 200, terminal state `completed`, health populated for both components. Not a 500.

**The negative result: with the S3 transform applied, I could not construct a graph that yields an
UNHEALTHY component through the run API.** Three attempts against the live stack, all of which
came back healthy or removed:

| graph | what happened |
|---|---|
| `local.file` reading a path that does not exist in the read-only sandbox | `sim_secret_source` → `secret_node_removed`; the component never reaches Alloy |
| `loki.source.file` tailing a nonexistent path → `loki.write` | `discovery_stub` → `log_source_stubbed`; Alloy tails the harness fixture instead, `healthy` |
| `otelcol.receiver.otlp` bound to an unbindable address (`0.0.0.0:80` as uid 65532, then `192.0.2.1:4317`), tested directly against the bare Alloy image | Alloy reports `{"state":"healthy","message":"started scheduled components"}` in both cases |

That is not an accident: the transform stubs or removes precisely the component classes that
report unhealthy at runtime (every discovery/log source, `local.file`, `remote.http`), and the
otelcol wrapper reports healthy regardless. **So the sticky-unhealthy branch in
`internal/simsvc/runner.go` — the code that keeps an unhealthy observation from being overwritten
by the benign "exited" state every component reports at shutdown — is currently unreachable from
the run API, and it had no test at all.**

Rather than leave it carried on an argument, it is now pinned at the level where it IS reachable
(`internal/simsvc/runner_test.go`), with its own red run:

```
$ # delete the sticky `continue` from healthTracker.observe
$ go test ./internal/simsvc/
  [FAIL] healthTracker [It] keeps an unhealthy observation when shutdown flips the component
         to a benign state
  FAIL! -- 78 Passed | 1 Failed
$ # restore
$ go test ./internal/simsvc/
ok  	shepherd/internal/simsvc	1.905s
```

**Stated as a residual, not implied:** "an unhealthy component completes with its state shown" is
proved for the tracker and for the API's refusal to 500, but has never been observed end to end
with real Alloy actually reporting `unhealthy`, because nothing in reach of a user's graph makes
real Alloy report it. If a future Alloy or a widened keep list changes that, this is the paragraph
to delete.

---

## 5. Two timing facts the specs now encode, both learned the hard way

1. **`scrape_interval` must be well under the run duration.** The committed spec used the corpus
   graph's `30s` against an `18s` run. It passed on one clean-stack run and failed on the very
   next one with `captured_series: 0` — Prometheus's first scrape is jittered within the interval,
   so whether an 18s run catches one is a coin flip. A capture assertion that flips on scrape
   jitter proves nothing on the run where it passes. Now `5s`.
2. **`scrape_timeout` must be under `scrape_interval`.** Alloy's default is `10s` and it refuses
   to build a scrape whose timeout exceeds its interval, failing the whole run at load (§2). So
   lowering the interval without lowering the timeout trades a flake for a hard red. Now `2s`.

Both are commented at the props in `e2e/sandbox_sim_test.go` naming the failure they prevent.

---

## 6. What CI runs now

`make e2e-sim` runs **two** ginkgo passes over one stack, and
`.github/workflows/e2e.yml`'s sandbox job runs that target instead of the narrower
`make e2e-egress` it used while M13 was open:

```
ginkgo --fail-on-empty --label-filter=sandbox-egress ./e2e/...                      # containment
ginkgo --fail-on-empty --label-filter='sandbox-sim && !sandbox-egress' ./e2e/...    # delivery
```

Two passes rather than one `--fail-on-empty` pass over `sandbox-sim`, deliberately: a single pass
would stay non-empty on the strength of the run-lifecycle specs alone, while every egress probe
had silently vanished — which is exactly the "green job measuring nothing" failure the
`--fail-on-empty` guard exists to prevent (finding H4). `!sandbox-egress` on the second pass
excludes the specs the first pass already ran, whose `Ordered` `BeforeAll` creates a fixed-name org
and would take an HTTP 409 the second time.

Clean-stack transcript of both passes is in §7.

**Known limitation, for anyone debugging with `E2E_KEEP=1`:** the sandbox scenarios create orgs
by fixed name, so a second run of the same filter against a stack that was not torn down fails in
`BeforeAll` with `Expected <int>: 409 to equal <int>: 201`. `make e2e-sim` always does
`docker compose down -v` first, so CI never sees it.

---

## 7. Verification transcript

Clean stack (`down -v` then `--profile sim up -d --wait`), both passes, current source:

```
$ go vet -tags e2e ./e2e/...
VET_OK

$ go run github.com/onsi/ginkgo/v2/ginkgo --tags=e2e --randomize-all=false \
      --fail-on-empty --label-filter=sandbox-egress ./e2e/...
SUCCESS! -- 8 Passed | 0 Failed | 0 Pending | 26 Skipped
Ginkgo ran 1 suite in 55.229004208s
Test Suite Passed

$ go run github.com/onsi/ginkgo/v2/ginkgo --tags=e2e --randomize-all=false \
      --fail-on-empty --label-filter='sandbox-sim && !sandbox-egress' ./e2e/...
SUCCESS! -- 6 Passed | 0 Failed | 0 Pending | 28 Skipped
Ginkgo ran 1 suite in 47.282397167s
Test Suite Passed
```

The 8 in the first pass include the runtime-retarget spec, whose middle assertion is new and was
impossible before §1's fix: the retarget run must reach `completed` (it used to tolerate
`BeElementOf("completed","failed")` because the wire could not run at all) and every captured
series must carry `instance=<canary-ip>:8080`. Measured on the live stack:

```
status: completed
rewrites: ['target_address_forced', 'destination_endpoint']
health:   [('sink','healthy'), ('retarget','healthy'), ('scrape','healthy')]
series: 5
  scrape_duration_seconds  {instance=192.168.117.3:8080, job=retarget, o1=192, o2=168, o3=117, o4=3}  5
  scrape_samples_post_metric_relabeling {instance=192.168.117.3:8080, ...}                            5
  scrape_samples_scraped   {instance=192.168.117.3:8080, ...}                                         5
  scrape_series_added      {instance=192.168.117.3:8080, ...}                                         5
  up                       {instance=192.168.117.3:8080, ...}                                         5
```

`target_address_forced` was disclosed — and then overridden at runtime by the relabel rule, which
is the whole finding: the sandbox really did aim five scrapes at the canary's address, an address
that appears nowhere in the rendered config. The transform did not contain it. The network did.

The 6 in the second pass are the run-lifecycle specs: the `minimal-scrape` submission reaching
`completed`, the captured-series assertion on `shepherd_sim_requests_total`, the all-green health
tab, the stderr-tail assertion (new — it requires Alloy's own `"Alloy is running"` line, so it
cannot pass on an empty string or on Shepherd's own log), the rewrite-disclosure `ConsistOf`, and
the captures-nothing-still-completes path.

`go run github.com/onsi/ginkgo/v2/ginkgo` rather than a `ginkgo` on PATH: the CLI installed on this
machine is 2.32.0 against the module's 2.32.1, and ginkgo warns about the mismatch. CI installs the
version-matched CLI (`.github/workflows/e2e.yml`), so `make e2e-sim`'s bare `ginkgo` is correct
there.

Unit-level, same source:

```
$ go test -count=1 ./internal/simulate/ ./internal/schema/ ./internal/visual/ ./internal/simsvc/
ok  	shepherd/internal/simulate	14.550s
ok  	shepherd/internal/schema	3.463s
ok  	shepherd/internal/visual	9.127s
ok  	shepherd/internal/simsvc	2.281s
```
