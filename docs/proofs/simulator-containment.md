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

Containment probes on that same network and image:

```
$ docker network inspect shepsim-verify-net -f '{{.Internal}}'
true

$ docker run --rm --network shepsim-verify-net --entrypoint /bin/sh grafana/alloy:v1.18.1 \
    -c 'getent hosts example.com'
egress-probe exit=2                          # egress denied

$ docker run --rm --network shepsim-verify-net --entrypoint /bin/sh grafana/alloy:v1.18.1 \
    -c 'getent hosts shepsim-verify'
fd07:b51a:cc66:d001::2 shepsim-verify        # positive control: the probe IS on that network

$ docker run --rm --entrypoint /bin/sh shepherd-simulator:local -c echo
docker: … exec: "/bin/sh": stat /bin/sh: no such file or directory
```

The positive control matters: without it, the egress probe could "pass" by being misconfigured.

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
