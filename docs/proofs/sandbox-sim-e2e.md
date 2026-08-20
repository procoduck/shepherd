# Wiring the `sandbox-sim` e2e scenario to a real simulator

**Scope.** VB-1 design doc §7.8 item 2 / §11 item 9 / §13 milestone 7: bring `dev`/`e2e` compose up
with the `sim` profile, and prove an S3 sandbox run against the REAL `shepherd-simulator` container
(real Alloy v1.18.1 as a child process), not a mock.

**Claims proved below, against real containers, not inspection:**

1. The dev/e2e compose wiring (Makefile targets, `sim` profile, `SHEPHERD_SIMULATOR_*` env) actually
   lets Shepherd reach the simulator's control API and run a config through real Alloy end to end.
2. The destination-rewrite + endpoint-allowlist containment the `sandbox-sim` scenario exists to test
   is real: breaking the rewrite makes captured series come back EMPTY; restoring it makes them
   non-empty again (the kill probe).
3. A separate, pre-existing defect in `internal/visual/render.go` — out of this stage's scope — blocks
   the specific `minimal-scrape` corpus graph the design doc names, and blocks it for a reason that has
   nothing to do with anything this stage built.

---

## 1. The compose wiring gap, and the fix

`config.SimulatorConfig`'s defaults (`internal/config/config.go`) all assume a compose service named
`shepherd-simulator` — literally the design doc's own name for it (§6.2: "**Simulator service**
(`shepherd-simulator`)"). But `internal/simsvc/compose_containment_test.go` pins the compose service's
key to `simulator`, and that test cannot be relaxed without weakening what it's actually checking. So
every one of `config.go`'s `SHEPHERD_SIMULATOR_*` defaults silently pointed at a DNS name (
`shepherd-simulator`) that does not exist on `sim-internal` — a run would have dialed nothing.

Fixed by setting the five `SHEPHERD_SIMULATOR_{CONTROL_URL,CAPTURE_BASE_URL,OTLP_GRPC_ADDRESS,
SYSLOG_HOST,TARGET_ADDRESS}` env vars explicitly on the `shepherd` service in both
`dev/docker-compose.dev.yaml` and `e2e/docker-compose.e2e.yaml`, pointing at `simulator` (the real
service key).

That was not the whole gap. `internal/simsvc/config.go`'s OWN endpoint allowlist
(`CheckEndpoints`/`guard.go` — the harness's second, independent containment gate, §6.4 defense in
depth) defaults to the literal `shepherd-simulator` PLUS `os.Hostname()`. Docker does not set a
container's hostname to its compose service name — `os.Hostname()` inside the simulator container
returns the container ID:

```
$ docker inspect e2e-simulator-1 --format '{{.Config.Hostname}}'
e33632720d9a
```

So neither half of that default matched `simulator` either, and the first real run failed before Alloy
even started:

```
error_code=internal
error_message=endpoint_not_allowed: config names host(s) outside the sandbox harness: simulator
```

Fixed the same way: `SIM_ALLOWED_HOSTS=simulator,localhost,127.0.0.1,::1` on the `simulator` service in
both compose files, overriding the built-in default that `allowedHosts()` (`internal/simsvc/config.go`)
already exists to support.

`go test ./internal/simsvc/... -run TestSimsvc` still passes after both changes — the containment spec
does not pin these values, only the security-relevant compose keys (profile, cap_drop, read_only,
tmpfs, network isolation, etc.).

---

## 2. Real end-to-end proof (both fixes in place)

Stack: `SHEPHERD_SIM_ENABLED=true docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d
--wait`, images rebuilt from current source (`shepherd:local`, `shepherd:local-init`,
`shepherd-simulator:local`/`:dev`).

A run submitted through the real REST API (`POST /api/orgs/{org}/simulate/runs`, org created through
`/api/admin/orgs`, same auth path e2e scenario 2 already uses) reaches `completed`, with Alloy's own
stderr in the row:

```
ts=...Z level=info msg="Starting WAL watcher" ... url=http://simulator:9110/capture/prometheus/api/v1/write
ts=...Z level=info msg="{^_^} Alloy is running"
```

captures real series (21, including the synthetic exporter's own counter and Prometheus's own
scrape-internal series), and reports all components healthy:

```json
{
  "status": "completed",
  "rewrites": [{"node_id":"n2","node_label":"sink","component":"prometheus.remote_write",
                "kind":"destination_endpoint",
                "detail":"endpoint re-pointed at the simulator's capture receiver"}],
  "captured_series": [ /* 21 entries */ ],
  "component_health": [
    {"node_id":"n2","component":"prometheus.remote_write","health_state":"healthy","message":"started component"},
    {"node_id":"n1","component":"prometheus.scrape","health_state":"healthy","message":"started component"}
  ]
}
```

One captured series is `shepherd_sim_requests_total{job="app",instance="simulator:9111",...}` — `job`
comes from the authored `job_name` prop, `instance` from Prometheus's own scrape-time relabeling of
`__address__` (not authored anywhere, and not emitted by the exporter — the "relabel-produced label"
the `sandbox-sim` scenario's spec checks for).

The "captures nothing but still completes" edge case is real too: `prometheus.scrape` pointed at zero
targets completes cleanly with BOTH components reporting `"health_state":"healthy"` — 0 targets does
NOT flip Alloy to unhealthy. This directly confirms VB-1 §6.4's hard constraint in the other direction
from the usual example: not just "an unreachable destination stays healthy while retrying", but "an
idle source with nothing to scrape also stays healthy" — health is not evidence of delivery in either
direction, and the UI/tests must key on captured series, never on health.

---

## 3. The kill probe

**The line under test** (`internal/simulate/transform.go`):

```go
func rewriteDestinations(doc *visual.GraphDocument, req TransformRequest) ([]Rewrite, TransformErrors) {
	var rewrites []Rewrite
	var errs TransformErrors
	...
```

Disabled by inserting `return nil, nil` as the function's first line, rebuilding `shepherd:local`, and
restarting the `shepherd` container in place (simulator untouched — this exercises Shepherd's own
transform, not the harness).

**Graph:** the same `prometheus.scrape` (literal `targets`, no wired discovery source) →
`prometheus.remote_write` (authored endpoint `https://prometheus.example.com/api/v1/write`) pair from
§2, submitted again unchanged. A literal-`targets` graph is used here instead of `minimal-scrape`'s
discovery-stub wiring for the reason §4 explains — the mechanism under test (rule D, the destination
rewrite, and the harness's independent endpoint allowlist) is identical either way; discovery stubbing
is rule G, a different rule, untouched by this probe.

### RED — `rewriteDestinations` disabled

```json
{
  "status": "failed",
  "error_code": "internal",
  "error_message": "endpoint_not_allowed: config names host(s) outside the sandbox harness: prometheus.example.com",
  "rewrites": [],
  "captured_series": []
}
```

Zero rewrites recorded (rule D never ran, as expected), and — the assertion that matters — **zero
captured series**. Containment held anyway: with the rewrite gone, the config still names
`prometheus.example.com`, and the harness's OWN independent allowlist (`internal/simsvc.CheckEndpoints`,
`guard.go`) refused to even start Alloy. This is defense in depth working exactly as designed (§6.4:
"a text gate must never be the only thing standing between user code and a real endpoint") — the
primary control (network isolation) plus this second gate both had to hold, and did, even with the
transform's own rewrite broken.

### GREEN — restored

`rewriteDestinations` reverted, `shepherd:local` rebuilt, container restarted, same graph resubmitted:

```json
{
  "status": "completed",
  "rewrites": [{"kind": "destination_endpoint", ...}],
  "captured_series": [ /* 21 entries, including shepherd_sim_requests_total */ ],
  "component_health": [
    {"health_state": "healthy", ...},
    {"health_state": "healthy", ...}
  ]
}
```

`go build ./...` and `go test ./internal/simulate/...` both green after the revert; the diff that
shipped is a no-op against `main`.

---

## 4. Blocking defect: `minimal-scrape` cannot complete against real Alloy (out of this stage's scope)

The design doc names `minimal-scrape` as the `sandbox-sim` scenario's graph (§7.8: "submit
`minimal-scrape` for a real run"), and `e2e/sandbox_sim_test.go` submits it exactly as committed at
`internal/visual/testdata/corpus/minimal-scrape.graph.json`. Doing so end to end fails:

```json
{
  "status": "failed",
  "error_code": "gate_failed",
  "error_message": "alloy_start_failed: alloy_start_failed: alloy exited during the run: exit status 1"
}
```

stderr:

```
Error: /tmp/.../config.alloy:8:14: discovery.relabel.k8s.output target::ConvertFrom: conversion
from '[]discovery.Target' is not supported

7 | prometheus.scrape "app" {
8 |   targets = [discovery.relabel.k8s.output]
  |              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^
9 |   forward_to = [prometheus.remote_write.sink.receiver]
```

**Root cause, isolated with hand-written `.alloy` files against the real `grafana/alloy:v1.18.1`
image** (not through Shepherd at all — this is a renderer/Alloy interaction, not a transform bug):

| Config | `alloy validate` | `alloy run` |
|---|---|---|
| `targets = discovery.relabel.k8s.output` (bare, no brackets) | exit 0 | **runs cleanly** |
| `targets = [discovery.relabel.k8s.output]` (bracket, 1 source) | exit 0 | **fails**: `ConvertFrom` |
| `targets = [discovery.kubernetes.k8s.targets]` — `minimal-scrape.golden.alloy`, byte for byte | exit 0 | **fails**: same error |
| `targets = [a.targets, b.targets]` (bracket, 2 sources, either component) | exit 0 | **fails**: same error, second element |
| `targets = array.concat(a.output, b.output)` | exit 0 (deprecation warning for the old `concat()` spelling) | **runs cleanly** |

So: **`alloy validate` cannot see this bug at all** — every one of the failing rows above validates
clean. Only `alloy run` (what the transform/harness actually does, and what
`internal/simulate/transform_validate_test.go`'s `Stages12` gate does NOT reach — it stops at
validate) evaluates the dataflow graph and hits the real `[]discovery.Target` conversion. The corpus
golden tests and `docs/proofs/transform-secret-drop.md`'s own worked example both check `alloy
validate` only, which is why this was not caught before.

The defect is in `internal/visual/render.go`'s `nodeRefs` (VB-1's own S3 transform work reuses this
function unmodified, per this stage's brief: "EXISTING, DO NOT REBUILD ... internal/visual/render.go —
GraphDocument/GraphNode/GraphEdge/GraphBinding/BindingRef types and the renderer"):

```go
value := texts[0]
if in.Cardinality == "list" {
    value = "[" + strings.Join(texts, ", ") + "]"
}
```

Every input in the shipped schema has `cardinality: "list"` (`docs/reviews/graph-model-and-validation.md`
§3.4: "103/103"), so this bracket-wraps unconditionally — for one wire or many, and for every port
type. It happens to be correct for `Receiver`-typed ports (`forward_to = [a.receiver]` runs fine,
confirmed above — `prometheus.remote_write.sink` reaches "Starting WAL watcher" before the run fails at
`prometheus.scrape`) and wrong for `[]discovery.Target`-typed ports, which is exactly the
`sources → prometheus.scrape`/`loki.source.*` wiring `minimal-scrape` — and every realistic scrape or
log pipeline in this product — depends on. This is not narrow to the simulator: the same rendered text
would reach real fleet Alloy agents through the ordinary pipeline-delivery path with the same defect,
undetected there for the identical reason (nothing in that path runs `alloy run` against the rendered
config either).

**Not fixed here.** `render.go` is explicitly out of this stage's scope, the correct fix is type-aware
(bracket for `Receiver`, bare-or-`array.concat` for `discovery.Target`, and there may be other affected
types among the 103 list-cardinality inputs this investigation didn't enumerate), and getting that
wrong risks a worse, silent regression across the whole rendering path — a bigger and different piece
of work than this stage's brief. `e2e/sandbox_sim_test.go`'s first spec submits `minimal-scrape`
exactly as designed and is RED for this reason today; it is not a self-skipping or falsely-passing
test — it fails for a real, reproducible reason, and will pass unmodified the moment the render defect
above is fixed. §2 and §3 of this doc prove that everything this stage actually owns — the compose
wiring, the containment allowlist, the destination-rewrite kill probe, and the captured-series/health
reporting round trip — works for real, using a graph shape that routes around the one part that isn't
this stage's to fix.
