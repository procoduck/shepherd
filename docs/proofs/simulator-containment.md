# Red–green proofs: shepherd-simulator and its containment

Behavior proved: the S3 sandbox service (`cmd/shepherd-simulator`, `internal/simsvc`) captures
what a real Alloy run emits, and every containment key in the compose stacks is load-bearing.

Each red run below removes exactly one control or one decode step and records the failure, so
none of the assertions can be green while the thing it claims is absent.

---

## Live verification (before any red run)

`grafana/alloy:v1.18.1` running as a child of the simulator, under the full containment set:

```
docker run -d --name shepsim-verify --network shepsim-verify-net \
  --user 65532:65532 --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --memory 512m --memory-swap 512m --cpus 1.0 --pids-limit 256 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=128m \
  shepherd-simulator:local serve
```

`docker inspect` of the running container:

```
ReadonlyRootfs=true
CapDrop=[ALL]
SecurityOpt=[no-new-privileges:true]
Memory=536870912
MemorySwap=536870912
NanoCpus=1000000000
PidsLimit=256
User=65532:65532
Ports=map[]
Networks=shepsim-verify-net
```

A 20-second run submitted to `POST /v1/runs` completed with real captures:

```
state: completed error: None
series: 21
   shepherd_sim_requests_total {'method':'GET','path':'/api/collectors','sim_marker':'shepherd'} samples=7 32 -> 80
   shepherd_sim_requests_total {'method':'GET','path':'/api/health','sim_marker':'shepherd'}     samples=7  8 -> 20
   shepherd_sim_queue_depth    {'sim_marker':'shepherd'}                                          samples=7  0 -> 4
log_lines: 200
   {'filename':'/tmp/shepherd-sim/logs/k8s-pod-logs.log','job':'simlogs'} '{"level":"info",…}'
components:
   prometheus.remote_write.cap -> node: node-dest   healthy started component
   prometheus.scrape.sim       -> node: node-scrape healthy started component
stderr_tail: 57
```

`sim_marker` is produced by a `discovery.relabel` rule, not by the exporter, so its presence in
the captured labels is the proof that scrape → relabel → remote_write executed for real. The
counters advance between scrapes (`32 -> 80`), which is what §6.4 step 2 requires.

The image carries no shell, which is a separate control and still hand-checked:

```
$ docker run --rm --entrypoint /bin/sh shepherd-simulator:local -c echo
docker: … exec: "/bin/sh": stat /bin/sh: no such file or directory
```

---

## P0 — egress denial, executed (finding H4)

**This is THE reachability control for the S3 sandbox, not a backstop to one.** The transform's
keep lists and the simulator's `CheckEndpoints` bound which authored VALUES leave a user's graph;
neither can bound where a running config connects, because a `discovery.relabel` rule computes
`__address__` at runtime from label data and the address need never appear as a literal anywhere.
The static rule that used to claim otherwise (`internal/simulate` rule P5, `address_not_harness`)
has been **deleted** — it could not see that graph and refused five ordinary relabel paths;
`docs/proofs/transform-address-closure.md` has both halves as executed red runs. If this section
is red, the sandbox has no reachability containment.

This section used to be a hand-run `getent` transcript. That was the defect finding H4 named:
egress denial was **configured** everywhere and **verified by execution** nowhere, while
`internal/simsvc/compose_containment_test.go` asserted only the YAML declaration and its own
header comment claimed an e2e probe existed that did not.

It exists now, in `e2e/sandbox_egress_test.go`, labelled `sandbox-egress`, run by
`make e2e-egress`, run in CI by `.github/workflows/e2e.yml`'s `e2e-egress` job — on the merge
queue and, since this control became load-bearing, on any pull request touching the containment
surface (`internal/simsvc`, `internal/simulate`, `internal/netshape`, the compose files, the
simulator image). `make e2e-egress` passes `ginkgo --fail-on-empty`, so a renamed `Label()` or a
deleted spec fails the job instead of leaving it green with "Ran 0 of N Specs" — finding H4's
failure mode wearing a different hat. The "outside" is a hermetic `egress-canary` service on the
default `shepherd-e2e` network only — no real internet traffic and no public address anywhere in
the repo.

### The runtime-retarget spec

Added with this revision, and the reason the section above is worded the way it is. It submits a
graph whose `discovery.relabel` rule writes `target_label = "__address__"` with a `replacement`
assembled from four ordinary label values holding the canary's octets — so no host-shaped token
exists anywhere in the config — and asserts both halves of the real architecture:

- the run is **accepted**: no transform refusal (`error_code != "cannot_stub"`), proven against
  the live pipeline. A future static gate that starts refusing it turns this red;
- the address it steers at is **unreachable** from the sandbox's own network namespace, while the
  identical dial from `shepherd-e2e` succeeds.

Since finding M13 was fixed (`docs/proofs/sandbox-sim-e2e.md` §1) it also asserts the middle link
by EXECUTION: the run reaches `completed` and every captured series comes back labelled
`instance=<canary-ip>:8080` — the address the transform never saw — rather than the harness's
`simulator:9111` that `target_address_forced` pointed at. Non-containment is now demonstrated by
the sandbox's own output, not inferred from the rendered text.

It still does NOT judge reachability from captured series, because they cannot judge it: a denied
scrape and a scrape that reached the canary yield the same five series (`up` plus Prometheus's
four scrape internals — the canary answers `ok`, which is not parseable Prometheus text). That is
what the out-of-band probes are for. The unit half — that the transform accepts the graph and
leaves `target_label = "__address__"` in the render — is
`internal/simulate/transform_address_test.go`'s *"Transform: a runtime retarget is NOT contained
by the transform"*.

`--network container:<simulator>` is the sandbox's *actual* namespace, not a stand-in for it:
`internal/simsvc/runner.go` starts Alloy with `exec.CommandContext` inside the simulator
container, so the sandbox has no network namespace of its own.

### Green — the shipped compose file (`internal: true`)

**Transcript provenance:** the run below predates the three runtime-retarget specs added in this
revision, so its spec counts are 5-of-30. It is left verbatim rather than edited to match, because
a transcript nobody ran is worth less than a stale one that was. The current spec count under the
same filter, from a dry run (no Docker stack, so no probe results — counts only):

```
$ ginkgo --tags=e2e --randomize-all=false --fail-on-empty --dry-run --label-filter=sandbox-egress ./e2e/...
Will run 8 of 33 specs
Ran 8 of 33 Specs in 0.000 seconds
SUCCESS! -- 8 Passed | 0 Failed | 0 Pending | 25 Skipped
```

And `--fail-on-empty` earning its place — one character removed from the label:

```
$ ginkgo … --fail-on-empty --dry-run --label-filter=sandbox-egres ./e2e/...
--- FAIL: TestE2E (0.00s)
Test Suite Failed
```

Without that flag the same typo reports "Ran 0 of 33 Specs / SUCCESS" and the CI job goes green
having measured nothing.

```
$ make e2e-egress
ginkgo --tags=e2e --randomize-all=false --label-filter=sandbox-egress ./e2e/...
Running Suite: Shepherd E2E Suite - /Users/.../shepherd/e2e
Will run 5 of 30 specs
••••SSSSSS•SSSSSSSSSSSSSSSSSSS

Ran 5 of 30 Specs in 29.166 seconds
SUCCESS! -- 5 Passed | 0 Failed | 0 Pending | 25 Skipped
```

(The fifth spec on that filter is CRITICAL-2's probe graph run end to end — see
`docs/proofs/transform-address-closure.md`.)

The four core probes run by hand, so the exit statuses the specs assert on are visible:

```
$ SIM=$(docker compose -f e2e/docker-compose.e2e.yaml --profile sim ps -q simulator)
$ CANIP=$(docker inspect -f '{{ (index .NetworkSettings.Networks "shepherd-e2e").IPAddress }}' e2e-egress-canary-1)
simulator=6db4c7221326… canaryIP=192.168.117.8

=== P-control  (--network shepherd-e2e) ===
$ docker run --rm --network shepherd-e2e alpine:3.22 wget -T 3 -q -O - http://egress-canary:8080/
ok (exit=0)

=== P-deny-name  (--network container:$SIM) ===
$ docker run --rm --network container:$SIM alpine:3.22 wget -T 3 -q -O - http://egress-canary:8080/
wget: bad address 'egress-canary:8080'
(exit=1)

=== P-deny-ip  (--network container:$SIM) ===
$ docker run --rm --network container:$SIM alpine:3.22 wget -T 3 -q -O - http://192.168.117.8:8080/
wget: can't connect to remote host (192.168.117.8): Network unreachable
(exit=1)

=== P-topology ===
$ docker inspect -f '{{ json .NetworkSettings.Networks }}' $SIM   # keys
['shepherd-e2e-sim']
$ docker network inspect -f '{{ .Internal }}' shepherd-e2e-sim
true
```

P-control is what stops the two denials being vacuous. Same image, same command, same canary —
only the network differs, so "it worked there and not here" can only be about the network.

### Red — `internal: true` flipped to `false` in the shipped compose file

The mutation is one character of intent in `e2e/docker-compose.e2e.yaml`:

```
   sim-internal:
     name: shepherd-e2e-sim
-    internal: true
+    internal: false
```

then `docker compose --profile sim down -v && … up -d --wait` (the network must be recreated for
the flag to take) and the same specs:

```
$ docker network inspect -f '{{ .Internal }}' shepherd-e2e-sim
false

$ ginkgo --tags=e2e --label-filter=sandbox-sim --focus="cannot reach anything off its own network" ./e2e/...
• [FAILED] [0.315 seconds]
Scenario sandbox-sim: the sandbox cannot reach anything off its own network
[It] P-deny-ip: the canary is NOT reachable by literal IP from the sandbox's network namespace

  [FAILED] the sandbox reached 192.168.117.3 by address — sim-internal routes off its own subnet. probe output:
  ok
  Expected an error to have occurred.  Got:
      <nil>: nil
  In [It] at: e2e/sandbox_egress_test.go:98

Ran 3 of 29 Specs in 1.422 seconds
FAIL! -- 2 Passed | 1 Failed | 0 Pending | 26 Skipped
```

P-topology, run on its own because the `Ordered` container aborts after the failure above:

```
$ ginkgo --tags=e2e --label-filter=sandbox-sim --focus="P-topology" ./e2e/...
  [FAILED] shepherd-e2e-sim is not an internal network, so the sandbox has a route off it
  Expected
      <string>: false
  to equal
      <string>: true
  In [It] at: e2e/sandbox_egress_test.go:117

Ran 1 of 29 Specs in 0.560 seconds
FAIL! -- 0 Passed | 1 Failed | 0 Pending | 28 Skipped
```

Raw probes under the mutation:

```
=== P-deny-name (non-internal) ===
wget: bad address 'egress-canary:8080'
(exit=1)                                  # STILL DENIED — see below

=== P-deny-ip (non-internal) ===
ok (exit=0)                               # REACHED THE CANARY
```

**Stated plainly, because it is the most important line in this document:** P-deny-name stays
green when `internal: true` is removed. Docker's embedded DNS on `shepherd-e2e-sim` does not
resolve a container that is not attached to `shepherd-e2e-sim`, whether or not the network has a
gateway. So P-deny-name proves DNS absence and nothing more; **P-deny-ip is the routing
assertion, and it is the probe that carries the containment claim.** A future change that kept
only the name probe would look like a passing containment suite and would be measuring nothing.
The spec comment on P-deny-name says so too.

### Green again — flipped back

```
$ docker network inspect -f '{{ .Internal }}' shepherd-e2e-sim
true
$ ginkgo --tags=e2e --label-filter=sandbox-sim --focus="cannot reach anything off its own network" ./e2e/...
Ran 4 of 29 Specs in 6.439 seconds
SUCCESS! -- 4 Passed | 0 Failed | 0 Pending | 25 Skipped
```

### What this proof does NOT cover

- **Kubernetes.** This paragraph used to say `grep -rn simulator deploy/helm/` was empty. It is
  no longer: `deploy/helm/shepherd/templates/{deployment,service,serviceaccount,networkpolicy}-simulator.yaml`
  exist, with default-deny egress and `automountServiceAccountToken: false`, asserted by
  `deploy/helm/chart_test.go`. What remains open on finding H5 is that a rendered-template
  assertion is not a probe: nothing here dials from inside a real cluster's simulator Pod the way
  `P-deny-ip` dials from inside the compose one. A green compose run says nothing about a
  Kubernetes deployment and must not be read as if it did.
- **The full S3 suite.** No longer an open item. Finding M13 — `internal/visual/render.go`'s
  list-cardinality bracket-wrap, which made `alloy run` refuse every discovery-to-scrape wire
  (`discovery.relabel.k8s.output target::ConvertFrom: conversion from '[]discovery.Target' is not
  supported`) — is fixed, and `make e2e-sim` now runs both the containment probes and the
  run-lifecycle specs green on a clean stack. CI runs `make e2e-sim`; red/green transcripts are in
  `docs/proofs/sandbox-sim-e2e.md`.

---

## P1 — `internal: true` removed from the sim network

Red (`dev/docker-compose.dev.yaml`, `internal: true` deleted):

```
[FAILED] sim-internal must be internal: true or the sandbox can reach the internet
Expected
    <bool>: false
to be true
internal/simsvc/compose_containment_test.go:116

Ran 64 of 64 Specs — FAIL! 63 Passed | 1 Failed
```

Green after restore: `ok shepherd/internal/simsvc`.

Without this key the network gets a gateway and every config a user runs regains full outbound
access — the exfiltration path §6.4's containment exists to close.

## P2 — `read_only: true` removed from the simulator service

Red (`e2e/docker-compose.e2e.yaml`):

```
Summarizing 1 Failure:
  [FAIL] Compose containment for the simulator service [It] e2e stack
Ran 64 of 64 Specs — FAIL! 63 Passed | 1 Failed
```

## P3 — OTLP gunzip branch disabled

Red (`internal/simsvc/capture_otlp.go`, the `Content-Encoding: gzip` branch short-circuited):

```
Summarizing 2 Failures:
  [FAIL] OTLP/HTTP capture [It] gunzips and decodes a real Alloy otlphttp metrics export
  [FAIL] OTLP/HTTP capture [It] accepts an uncompressed body too
Ran 64 of 64 Specs — FAIL! 62 Passed | 2 Failed
```

This is the measured failure mode: `otelcol.exporter.otlphttp` gzips by default, a plain proto
handler answers 400, and Alloy logs `Exporting failed. Dropping data` — an empty capture that
looks exactly like a broken pipeline.

## P4 — remote_write decoder drops a label

Red (`internal/simsvc/capture_prom.go`, `sim_marker` skipped while building the label map):

```
Summarizing 1 Failure:
  [FAIL] Prometheus remote_write capture [It] decodes a real Alloy remote_write body into the
         series it carries, including the relabel-produced label
Ran 64 of 64 Specs — FAIL! 63 Passed | 1 Failed
```

The golden bytes are real Alloy output (see `internal/simsvc/testdata/README.md`), so this
assertion fails for a decoder regression the way it would fail in production, not because the
test re-encoded what it decodes.

## P5 — capture path changed on one side only (not possible by construction)

Changing `simulate.CapturePathPrometheus` turned `internal/simulate` red (its transform specs
and goldens carry the emitted URL) while `internal/simsvc` stayed green:

```
--- FAIL: TestSimulate
FAIL	shepherd/internal/simulate
ok  	shepherd/internal/simsvc
```

That asymmetry is the point, and it is worth stating plainly: the harness routes cannot drift
from the transform's URLs because both read the same constants in
`internal/simulate/harness_paths.go`. The `simsvc` contract spec asserts the wiring, but the
compile-time coupling is what actually prevents the drift; a path typo is a change to one
constant, which the transform's goldens catch.

---

## Green run

```
$ go build ./...
$ go test -race ./internal/simsvc/
ok  	shepherd/internal/simsvc	2.463s
$ go test ./...
ok (all packages)
```
