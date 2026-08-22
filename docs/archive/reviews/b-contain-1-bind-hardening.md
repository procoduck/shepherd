# B-CONTAIN-1 — bind-address hardening decision record

> Decision record for the fix shipped 2026-08-21 (docs/project-status.md § B-CONTAIN-1).
> Option C below was implemented; A/B/D document what was considered and why it lost.
> The §4 diffs were the implementation sketch — the shipped compose files and
> internal/simsvc/compose_containment_test.go are authoritative, including one
> post-memo addition the sketch missed: shepherd's host-published ports are
> IPv4-loopback-only (127.0.0.1:...), because an IPv4-literal bind makes the
> socket IPv4-only while Docker publishes dual-stack — localhost resolves ::1
> first and the v6 proxy half resets after connect.


Author: W3 (read-only design pass). Scope: `e2e/docker-compose.e2e.yaml`,
`dev/docker-compose.dev.yaml` only. No repo files touched by this session.

## 1. The actual topology (verified against the tree, not the docs)

- Both compose files put `shepherd` on two networks: `default` (public, has a
  gateway) and `sim-internal` (`internal: true`, no gateway/egress).
  `simulator` is on `sim-internal` **only**. `shepherd`'s env then points the
  simulate transform at `simulator`'s harness ports over that network
  (`SHEPHERD_SIMULATOR_CONTROL_URL=http://simulator:8099`,
  `_CAPTURE_BASE_URL=...:9110`, `_OTLP_GRPC_ADDRESS=...:4317`,
  `_SYSLOG_HOST=simulator`, `_TARGET_ADDRESS=...:9111`) — e2e:186-189,
  dev:171-175.
- **The dataflow is already one-directional at the application layer.**
  `internal/simulate/client.go`'s `Client` only ever does `POST /v1/runs` and
  `GET /v1/runs/{id}` against `SimulatorConfig.ControlURL`
  (`internal/config/config.go:304`). Nothing in `internal/simsvc` or
  `internal/simulate` ever dials back into shepherd — the simulator has no
  config field that names a shepherd address. Confirmed by reading
  `internal/simulate/client.go` in full and `internal/config/config.go`'s
  `simulator.*` env bindings (lines 294–311): every one of them is
  shepherd-reads-simulator, none is simulator-reads-shepherd.
- **Alloy is not a sibling container — it is a bare child process of the
  `simsvc` process**, started with plain `exec.CommandContext` in
  `internal/simsvc/runner.go:90`. No `--network container:...`, no netns
  manipulation — it inherits the simulator container's network namespace
  because that's what `exec` does. `internal/simsvc/service.go`'s `Serve`
  (the same process) binds the control API (`:8099`), the capture receivers
  (`:9110` HTTP, `:9111` synthetic exporter, `:4317` OTLP gRPC, `:5514`
  syslog) all in the one process, all on `0.0.0.0` (`cfg.Listen` etc. default
  to bare `":port"` — `internal/simsvc/config.go:91-95`). The package doc
  (`internal/simsvc/service.go... package comment`, actually
  `internal/simsvc/config.go:1-12`) states this is deliberate: "every control
  the platform applies to this process applies to Alloy automatically, with
  no Docker socket in the picture." Splitting Alloy out of this process is
  explicitly the thing the design avoided.
- **Shepherd's own listeners are already address-configurable with zero Go
  changes needed.** `internal/server/server.go:111-123` builds `httpSrv` with
  `Addr: cfg.Server.Listen` and `metricsSrv` with
  `Addr: cfg.Server.MetricsListen` — plain `http.Server.Addr` strings.
  `internal/config/config.go:225,227` defaults them to `":8080"` / `":9090"`
  (all interfaces); `:266,268` bind them to
  `SHEPHERD_SERVER_LISTEN` / `SHEPHERD_SERVER_METRICS_LISTEN`. Go's
  `net.Listen("tcp", "1.2.3.4:8080")` binds only that interface — this is
  stdlib behavior already wired through config, not a feature to build.
- The named leak in `project-status.md` — "the sandbox scrapes Shepherd's
  unauthenticated metrics port" — is `:9090`/promhttp
  (`internal/server/server.go:223-230`, `newMetricsMux`, mounted only on the
  separate metrics listener, no auth middleware anywhere near it). The main
  API on `:8080` is behind OIDC/local-admin/bearer auth for essentially every
  route, so it's the metrics port that is the concrete, currently-provable
  disclosure; the main API surface is a smaller but non-zero secondary
  exposure (agent long-poll auth, health/ready) — enumerated in §5(D) below.
- `e2e/sandbox_egress_test.go` today proves only *outbound* denial from the
  sandbox's netns to an arbitrary host (the `egress-canary` on `default`,
  by name and by IP) and topology (`P-topology`: simulator's network set is
  exactly `{shepherd-e2e-sim}`, and that network is `Internal=true`). It has
  **no probe that dials shepherd** from the sandbox netns — B-CONTAIN-1 is
  not red-proven anywhere; only asserted in a compose-file comment
  (e2e:234-239 / dev:258-262, "RESIDUAL RISK, stated plainly").
- `internal/simsvc/compose_containment_test.go` pins the YAML declaration
  (`shepherd.Networks` contains `sim-internal`, `sim-internal.internal ==
  true`, the simulator's own hardening keys) but never asserts anything
  about what shepherd exposes on that network — nothing there would fail if
  this bug were reintroduced.
- `docs/proofs/simulator-containment.md` and
  `docs/archive/reviews/s3-sandbox-security-findings.md` both predate/pre-date a
  fully separate treatment of B-CONTAIN-1: the findings doc's focus is the
  *transform's* address-closure gaps (SSRF via literal targets, runtime
  relabel non-containment — all now handled by the network-is-the-control
  posture and `e2e/sandbox_egress_test.go`), not shepherd's own exposure on
  `sim-internal`. Neither document names shepherd's metrics port as reachable
  — this was caught later, straight from re-reading the compose file's own
  comment, which is why it is `project-status.md`'s current open item and
  not yet in either proof doc.
- `docs/kind-test-environment-plan.md` §5 Layer B already names the
  Kubernetes-side probe for this exact concern: **`P-shepherd` — denied —
  "closes B-CONTAIN-1: the sandbox cannot scrape the control plane"**. That
  row is not yet built (Layer B is unbuilt per the plan's status line), but
  it fixes the probe *name* this memo should mirror on the compose side, and
  it confirms the K8s answer is NetworkPolicy's directional Ingress/Egress
  split on the **same pod** — Kubernetes does **not** split simulator into
  two pods either (§1: "the sandboxed Alloy is a child process of the
  simulator pod, so the pod's network boundary is the sandbox boundary" —
  `project-status.md` B-CONTAIN-2 section, restated in the plan's §1). That
  is strong evidence against Option A below: even the environment that
  *can* express directional policy chose not to physically split the
  process, and compose can't express direction at all, so paying for a
  physical split there buys strictly less than K8s gets for free.

## 2. Options

### A) Split into `sim-control` + `sim-sandbox` containers, shared volume, no shared netns

**Shape:** `sim-control` (new network `sim-control-internal`, joined by
`shepherd` and `sim-control`) owns `Server`/`Queue`/`startRun`/`getRun`. It
writes the rendered config to a volume and needs the sandbox to actually run
it. `sim-sandbox` (alone on `sim-internal`, no ports, current hardening keys)
owns the capture harness (`Harness`, `NewSyntheticExporter`, all four
non-control listeners) and the Alloy child process, because the capture
receivers must share Alloy's netns for `discovery.relabel`-computed
addresses to reach them at all — they cannot live in `sim-control` without
re-opening exactly the reachability question this option exists to close.

**Forced code changes, concretely:**
- `internal/simsvc/queue.go`'s `Submit`/`serve` today call `runAlloy`
  in-process (`internal/simsvc/runner.go`). That has to become cross-container
  IPC: `sim-control` writes a run-request file (config + duration +
  component index) to the shared volume; `sim-sandbox` needs its own watch
  loop (new code, not in the tree today) that notices the file, execs Alloy
  exactly as `runAlloy` does now, and writes a result file back for
  `sim-control` to pick up on the next `GET /v1/runs/{id}` poll.
- `readiness()` (`internal/simsvc/service.go:147`) currently asserts one
  process's Alloy binary + writable dirs. It has to split: `sim-control`'s
  `/readyz` would need to probe the *other* container's health somehow (no
  network path exists between them by design — that's the whole point — so
  it would have to trust a heartbeat file on the shared volume instead).
- `Harness.begin/end` (`internal/simsvc/harness.go:39-54`) currently
  guarantees exactly one active sink because it's single-process,
  mutex-guarded. Split across containers, "one run at a time" becomes a
  cross-process invariant enforced through the same file-based queue —
  new code, new race surface (a `sim-control` crash mid-run needs a recovery
  path the current in-memory `Queue` doesn't need).
- `CheckEndpoints`/`AllowedHosts` (`internal/simsvc/guard.go`,
  `internal/simsvc/config.go:71-74`) is evaluated by `sim-control` today
  against the config it's about to hand to Alloy in the same process. Split,
  it's evaluated by a container that will never run the config it's
  checking — the gate and the execution are now on opposite sides of a
  filesystem handoff with no verification that what `sim-sandbox` executes
  is byte-identical to what was checked. That's a new integrity requirement
  (hash the file, or re-check in `sim-sandbox`) that doesn't exist today.
- `deploy/Dockerfile.simulator` becomes two Dockerfiles/build targets or one
  image with two entrypoints selected by an env var — new build surface,
  new `check-docker` guard cases.

**Containment achieved:** genuinely asymmetric — `sim-sandbox` has no route
to anything shepherd can reach, by construction, matching K8s's egress:[]
posture exactly. A `P-shepherd-deny` probe (dial shepherd from
`--network container:<sim-sandbox-id>`, expect failure) would be as solid as
`P-deny-ip` is today for the egress canary.

**Blast radius:** the largest of the four options by a wide margin — a new
IPC protocol, a new failure-recovery path, a new integrity check, a doubled
build/deploy surface, and every one of `internal/simsvc`'s current
single-process tests (`queue_test.go`, `runner_test.go`, `server_test.go`,
`guard_test.go`, `capture_test.go`) needs re-architecting around the
container boundary that used to be a function call.

**Failure modes:** IPC polling latency (a run's actual start lags the
control API's "queued" response by however long `sim-sandbox`'s poll
interval is — user-visible), split-brain on `sim-control` restart mid-run,
volume permission/ownership friction between two differently-UID'd
containers sharing one mount, and doubled cognitive load for anyone
debugging a run (which container failed?).

**e2e change required:** `sandbox_egress_test.go`'s `P-topology` today
asserts the simulator sits on exactly one network — that assertion inverts
entirely (now two containers, two networks, and the topology proof has to
show `sim-sandbox` has *zero* overlap with `sim-control-internal`). New
compose containment spec entries for two services instead of one.

**Verdict:** correct in principle, disproportionate in cost. It rebuilds, in
a local-dev/e2e-only environment, machinery Kubernetes gets from
NetworkPolicy for free — and the kind-test-environment-plan's own §1 already
notes K8s doesn't bother splitting the pod either. Reject as primary; note
in the memo for the record.

### B) Reverse control flow: simulator polls shepherd

**Why it doesn't fit:** the premise doesn't match the code. Today shepherd
is the *only* initiator (§1) — `internal/simulate/client.go` calls out,
`internal/simsvc` never calls back. There is no "control flow" pointed the
wrong way to reverse. Reversing it for real would mean shepherd exposing a
`GET /simulator/next-run` long-poll endpoint the simulator calls, and moving
`RunWorker`'s (`internal/simulate/worker.go`) submit/poll logic inside-out —
a rewrite of the run-lifecycle API contract (`shepherd.mgmt.v1` proto's
`SimulateRun` RPCs, `internal/mgmtapi/rpc_simulate.go`) for a network
topology problem that doesn't require touching the API layer at all.

Even granting the rewrite: it still doesn't remove shepherd from
`sim-internal`, because the simulator's poll target would itself need to be
an address shepherd listens on — and that address is reachable from the same
symmetric network the sandbox shares, so the sandbox can still scrape it
(now a `/simulator/next-run` endpoint carrying config text, arguably worse
than the metrics port). Reversing polling direction doesn't change compose
network symmetry; only removing shepherd's socket from that network's
reachable surface does. This is why Option C, not B, is the fix that
actually uses the asymmetry insight correctly.

**Verdict:** reject. Doesn't match the current architecture, requires an API
rewrite, and doesn't solve the stated problem even after the rewrite.

### C) Bind-address hardening (recommended)

**Mechanism, already half-built:** `cfg.Server.Listen` /
`cfg.Server.MetricsListen` are plain address strings all the way down to
`http.Server.Addr` (`internal/server/server.go:111-123`) — **zero Go code
changes needed**. Assign shepherd a static IP on `default` via compose's
`ipv4_address`, and set `SHEPHERD_SERVER_LISTEN=<that-ip>:8080` /
`SHEPHERD_SERVER_METRICS_LISTEN=<that-ip>:9090`. The listening sockets then
never bind on the `sim-internal` interface at all — a SYN arriving on that
interface addressed to shepherd's `sim-internal` IP, on either port, gets a
kernel-level RST (`connection refused`): no listening socket owns that
`(IP, port)` pair, independent of which interface the packet arrived on.

**Is it robust, or bypassable via the gateway?** Robust, and for a stronger
reason than "the socket isn't there": `sim-internal` is `internal: true`,
which per the existing comment (e2e:314-316) means **no gateway at all** —
Docker does not wire a route from that bridge to any other user-defined
network's subnet. Compose networks are separate Linux bridges; there is no
inter-bridge routing unless the host explicitly enables IP forwarding and
FORWARD rules between them, which compose does not do. So even before
considering socket binding, the `sim-internal` bridge has no L3 path to the
`default` bridge's subnet at all — shepherd's `default`-network IP is
**unroutable** from `sim-internal`, not merely unlisted. The bind-hardening
is defense in depth on top of that: it also closes the case this repo
already learned to distrust (B-CONTAIN-2) — an engine whose `internal: true`
implementation is looser than Docker's own (OrbStack's in-subnet gateway
reachability) — because even if `sim-internal` somehow gained a route to
`default`'s subnet, shepherd's ports still aren't bound there. **What it
does NOT fix:** host-published-port reachability via the OrbStack gateway
(B-CONTAIN-2) — that leak runs through the host's NAT/docker-proxy layer,
entirely orthogonal to which interface shepherd's own process binds. That's
correct and expected: B-CONTAIN-2 is tracked separately and is explicitly
scoped "local dev only, does not exist in Kubernetes" — this option isn't
supposed to touch it.

**Containment achieved:** the sandbox's own netns (shared with Alloy) has no
route to `default` at all, and even granting a hypothetical route, nothing
listens for it there. A `P-deny-shepherd` probe (naming matches
`docs/kind-test-environment-plan.md`'s `P-shepherd` row) dialing shepherd's
**`sim-internal` IP** on `:9090` and `:8080` from
`--network container:<simulator-id>` must fail; the same dial from the
`default` network to shepherd's `default` IP must succeed (the control half
— without it the denial probe is meaningless, per the existing
`P-control`/`P-deny-*` pairing pattern this file already uses).

**Blast radius — compose:**
- `e2e/docker-compose.e2e.yaml` and `dev/docker-compose.dev.yaml`, each:
  add `ipam.config.subnet` to `networks.default` and `networks.sim-internal`
  (neither currently declares one — a plain named network gets a
  Docker-assigned subnet, and `ipv4_address` requires a declared `ipam`
  block to pin against); add `services.shepherd.networks.default.ipv4_address`
  and `.sim-internal.ipv4_address`; set `SHEPHERD_SERVER_LISTEN` /
  `SHEPHERD_SERVER_METRICS_LISTEN` to the `default`-network literal.
- **No Go changes** in `internal/server`, `internal/config`, or anywhere
  else — the mechanism is already wired.
- `internal/simsvc/compose_containment_test.go`: extend the existing
  `DescribeTable` entry with new assertions — `shepherd.Environment` must
  carry `SHEPHERD_SERVER_LISTEN` and `SHEPHERD_SERVER_METRICS_LISTEN` set to
  an address that is NOT a bare `:port` (i.e., does not start with `:`) and
  is not `0.0.0.0:...`; parse the compose `networks.default.ipv4_address`
  literal and assert the env var's host half equals it exactly, so a future
  edit that pins a new IP without updating the listen address (or vice
  versa) fails loudly instead of silently reopening the port on every
  interface. This is the change that makes the control's *removal*
  red — deleting the env var override, or reverting `ipv4_address` to a
  different literal than the listen address, must fail this spec.
- `e2e/sandbox_egress_test.go`: new probes as described above.

**Failure modes:** subnet collision — two static subnets pinned across
`dev/docker-compose.dev.yaml` and `e2e/docker-compose.e2e.yaml` must not
overlap each other (both stacks can run concurrently per the e2e file's own
comment, e2e:155-157) or the host's own LAN/VPN ranges; pick distinct
private ranges per stack (e.g. e2e `172.28.0.0/24` / `172.28.1.0/24`, dev
`172.29.0.0/24` / `172.29.1.0/24`) and document why. A static IP also means
a `docker compose down -v && up` that changes network definition ordering
can't silently reassign shepherd a different address the way DHCP-style
Docker networking currently does — that's a feature here (determinism is
exactly what the containment test needs), not a cost. One more real edge:
Docker's own port-publish DNAT (`ports: "18080:8080"` in the e2e file) must
still land on the interface shepherd now explicitly binds — it does,
because Docker's published-port forwarding targets the container's IP on
whichever network carries its default route (here still `default`, since
`sim-internal` has none), which is the same IP the socket is now bound to.
Worth a smoke check (`curl localhost:18080/healthz` after `make e2e-sim
up`) during implementation, not a structural risk.

### D) Accept-and-document

**What stays exposed, enumerated exactly:** shepherd's promhttp `:9090`
(`internal/server/server.go:223-230`) — no auth, full process/Go runtime
metrics plus whatever custom collectors are registered (request counts by
route including org/pipeline identifiers if any label them) — is the
concrete finding named in `project-status.md`. Secondarily, shepherd's main
`:8080` — behind OIDC/local-admin/bearer for real routes, but `/healthz`
(`internal/server/server.go:292-298`) is deliberately unauthenticated by
design (liveness probe contract) and reachable too; low sensitivity
(constant "ok") but still a control-plane response the sandbox's own results
view could surface verbatim if a user built a scrape pointed at it,
alongside the already-documented `endpoint_not_allowed` gate (`guard.go`)
which does NOT block bare-hostname literals reaching shepherd by IP (per
`s3-sandbox-security-findings.md:99` — the allowlist "by design does not
inspect bare hostnames", the same class of gap that made the network the
real control in the first place).

**Would the fleet ever run compose in production?** No — confirmed by
`docs/kind-test-environment-plan.md` §1 ("Kubernetes is the production
target; compose is a local-development convenience") and
`project-status.md`'s B-CONTAIN-2 section ("Compose stays a
local-development convenience"). That's a real point in D's favor in the
abstract, but it doesn't change that F5 (S3) is explicitly gated: "Disabled
by default and must stay so until B-CONTAIN-1 closes" — the ledger already
committed to closing this, not documenting around it, and the fix
(Option C) costs less than the paperwork this option would require to keep
honest (updating three docs' "RESIDUAL RISK" language, the findings doc,
and re-justifying F5's gate rationale) without removing the disclosure risk
at all.

**Verdict:** reject — cheapest and already tried; it's the status quo the
ledger says must not stand.

## 3. Recommendation

**Option C — bind-address hardening.** It closes exactly the leak named in
`project-status.md` (shepherd's unauthenticated metrics port reachable from
the sandbox netns), costs zero Go code changes because the mechanism
(`Server.Listen`/`MetricsListen` as address strings) already exists, has the
smallest blast radius of the three real options, and doesn't fight the
documented architectural decision to run Alloy as a plain child process
(Option A would reverse that decision for a benefit Kubernetes itself
doesn't bother buying — see §1's `kind-test-environment-plan.md` citation).
Option B doesn't apply to the actual code. Option D is the status quo the
ledger has already committed to closing.

## 4. Exact diffs (Option C)

### `e2e/docker-compose.e2e.yaml`

```diff
   shepherd:
     ...
     environment:
       SHEPHERD_DATABASE_URL: postgres://shepherd:shepherd@postgres:5432/shepherd_e2e?sslmode=disable
+      # B-CONTAIN-1: bind to the default-network address only. sim-internal
+      # is internal:true (no gateway) so this address is unroutable from
+      # there regardless, but binding explicitly closes it even if a future
+      # engine's internal:true implementation is looser than Docker's own
+      # (see B-CONTAIN-2 — the OrbStack gateway-reachability gap this repo
+      # already hit once). Must equal networks.default.ipv4_address below.
+      SHEPHERD_SERVER_LISTEN: "172.28.0.10:8080"
+      SHEPHERD_SERVER_METRICS_LISTEN: "172.28.0.10:9090"
       SHEPHERD_SECURITY_ENCRYPTION_KEY: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
       ...
     networks:
-      - default
-      - sim-internal
+      default:
+        ipv4_address: 172.28.0.10
+      sim-internal:
+        ipv4_address: 172.28.1.10
     healthcheck:
       test: ["CMD", "/usr/local/bin/shepherd", "healthcheck", "--addr", "localhost:8080"]
...
 networks:
   default:
     name: shepherd-e2e
+    ipam:
+      config:
+        - subnet: 172.28.0.0/24
   sim-internal:
     name: shepherd-e2e-sim
     internal: true
+    ipam:
+      config:
+        - subnet: 172.28.1.0/24
```

(`healthcheck`'s `--addr localhost:8080` still works unmodified: the probe
runs from inside the container, and `localhost` there resolves to the
loopback interface, not to either bound address — Go's `http.Server` bound
to a specific non-loopback IP still also needs `localhost`/`127.0.0.1` to
work for the healthcheck to keep passing. **This is the one place the diff
needs a decision**: either (a) bind `Listen`/`MetricsListen` to
`0.0.0.0:8080`-style but pass a *second*, loopback-only listener for the
healthcheck path only — more code — or (b) keep the healthcheck's `--addr`
as `localhost:8080` only if `Server.Listen` binds are per-address and the
healthcheck CLI dials the same literal shepherd binds to. Concretely: change
the healthcheck invocation's `--addr` to the same `172.28.0.10:8080` literal
instead of `localhost:8080`, since binding to a specific non-loopback IP
means the loopback interface no longer has a listener there. This is a
required, easy-to-miss third line of the diff:)

```diff
     healthcheck:
-      test: ["CMD", "/usr/local/bin/shepherd", "healthcheck", "--addr", "localhost:8080"]
+      test: ["CMD", "/usr/local/bin/shepherd", "healthcheck", "--addr", "172.28.0.10:8080"]
```

Same three shapes of edit apply to the `simulator` service's healthcheck?
No — `simulator`'s healthcheck already dials `127.0.0.1:8099`
(unaffected — `simsvc`'s own listeners are untouched by this change, they
stay `0.0.0.0` because everything on `sim-internal` is *supposed* to reach
the simulator's control/capture ports; the asymmetry this fix introduces is
one-directional, shepherd-side only).

### `dev/docker-compose.dev.yaml`

Same three edits, distinct subnet so the two stacks can run concurrently
(e2e:155-157 already documents dev and e2e running side by side):

```diff
   shepherd:
     ...
     environment:
       SHEPHERD_DATABASE_URL: postgres://shepherd:shepherd@postgres:5432/shepherd_dev?sslmode=disable
+      SHEPHERD_SERVER_LISTEN: "172.29.0.10:8080"
+      SHEPHERD_SERVER_METRICS_LISTEN: "172.29.0.10:9090"
       ...
     networks:
-      - default
-      - sim-internal
+      default:
+        ipv4_address: 172.29.0.10
+      sim-internal:
+        ipv4_address: 172.29.1.10
     ports:
       - "8080:8080"
     healthcheck:
-      test: ["CMD", "/usr/local/bin/shepherd", "healthcheck", "--addr", "localhost:8080"]
+      test: ["CMD", "/usr/local/bin/shepherd", "healthcheck", "--addr", "172.29.0.10:8080"]
...
 networks:
   default:
     name: shepherd-dev
+    ipam:
+      config:
+        - subnet: 172.29.0.0/24
   sim-internal:
     name: shepherd-dev-sim
     internal: true
+    ipam:
+      config:
+        - subnet: 172.29.1.0/24
```

Also update both files' RESIDUAL RISK comments (e2e:234-239, dev:258-262):
replace "Putting shepherd on sim-internal so it can call the simulator also
lets the sandbox reach shepherd's API on that network" with the corrected
posture — shepherd is still a `sim-internal` member (needed for DNS/routing
to reach `simulator`), but no longer *listens* there; state the residual
risk that's actually left (B-CONTAIN-2's host-gateway path, which this
change does not touch).

### `internal/simsvc/compose_containment_test.go`

Add to the existing `DescribeTable` body (after the current `shepherd.Networks`
assertion, ~line 135):

```go
// B-CONTAIN-1: shepherd must not LISTEN on sim-internal even though it is
// a member of it — a control that removal of these two env vars (or
// reverting them to a bare ":port") silently defeats.
Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SERVER_LISTEN"))
Expect(shepherd.Environment).To(HaveKey("SHEPHERD_SERVER_METRICS_LISTEN"))
listenAddr := shepherd.Environment["SHEPHERD_SERVER_LISTEN"]
metricsAddr := shepherd.Environment["SHEPHERD_SERVER_METRICS_LISTEN"]
Expect(listenAddr).NotTo(HavePrefix(":"), "a bare :port binds every interface, including sim-internal")
Expect(metricsAddr).NotTo(HavePrefix(":"), "same — this is the exact leak B-CONTAIN-1 names")
Expect(listenAddr).NotTo(HavePrefix("0.0.0.0"))
Expect(metricsAddr).NotTo(HavePrefix("0.0.0.0"))
// The bound host must equal shepherd's OWN default-network address, and
// that address must differ from its sim-internal address — otherwise
// this whole check is checking nothing.
defaultIP := shepherdNetworkIP(doc, "default")   // new helper: reads services.shepherd.networks.default.ipv4_address
simIP := shepherdNetworkIP(doc, "sim-internal")
Expect(defaultIP).NotTo(BeEmpty())
Expect(simIP).NotTo(BeEmpty())
Expect(simIP).NotTo(Equal(defaultIP))
Expect(listenAddr).To(HavePrefix(defaultIP + ":"))
Expect(metricsAddr).To(HavePrefix(defaultIP + ":"))
```

This needs `composeService.Networks` reshaped from `[]string` to a type that
also captures the per-network `ipv4_address` map form (compose's short list
form and long map form are both valid YAML for `networks:`; the diff above
switches shepherd to the long form on purpose, so the struct's `yaml:"networks"`
tag needs to decode `map[string]struct{ Ipv4Address string
`yaml:"ipv4_address"` }` — a small, mechanical parser change to the existing
loader, not a new file).

### `e2e/sandbox_egress_test.go`

Add a fifth probe group mirroring the existing `P-control`/`P-deny-*` shape,
named to match `kind-test-environment-plan.md`'s `P-shepherd` row:

```go
It("P-shepherd-control: shepherd IS reachable on its own network", func() {
    out, err := probe("--network", e2eNetwork, shepherdDefaultAddr+":9090")
    Expect(err).NotTo(HaveOccurred(), "probe output:\n%s", out)
})

It("P-shepherd-deny: shepherd's metrics port is NOT reachable from the sandbox's network namespace", func() {
    out, err := probe("--network", "container:"+simulatorID, shepherdSimAddr+":9090")
    Expect(err).To(HaveOccurred(),
        "the sandbox reached shepherd's metrics endpoint at %s — B-CONTAIN-1 is back. probe output:\n%s",
        shepherdSimAddr, out)
})

It("P-shepherd-deny-api: shepherd's main API port is NOT reachable from the sandbox's network namespace", func() {
    out, err := probe("--network", "container:"+simulatorID, shepherdSimAddr+":8080")
    Expect(err).To(HaveOccurred(), "probe output:\n%s", out)
})
```

`shepherdDefaultAddr` / `shepherdSimAddr` come from
`docker inspect -f '{{ (index .NetworkSettings.Networks "shepherd-e2e").IPAddress }}' <shepherd-container-id>`
and the `shepherd-e2e-sim` equivalent, read in `BeforeAll` the same way
`canaryIP` is today (line 80-82) — not hardcoded, so the probes stay correct
even if the pinned subnet literals above are later changed.

**Red-proving it:** with the `SHEPHERD_SERVER_LISTEN` /
`SHEPHERD_SERVER_METRICS_LISTEN` overrides removed (reverting to the
`:8080`/`:9090` defaults), `P-shepherd-deny` and `P-shepherd-deny-api` must
flip to failing (probe succeeds, `Expect(err).To(HaveOccurred())` fails) —
that's the removal-makes-it-red proof the repo's testing standard requires
(`project-status.md` §6: "every control needs a test that fails when the
control is removed"). Confirm this by hand once during implementation
(comment the two env vars out, re-run `make e2e-egress`, observe red,
restore).

## 5. Step list for a single implementation agent

1. Pick and record the two subnet pairs (e2e `172.28.0.0/24` /
   `172.28.1.0/24`, dev `172.29.0.0/24` / `172.29.1.0/24`, or renumber if
   these collide with something in the actual CI/dev environment — check
   `docker network ls` output for existing overlaps before committing to
   literals).
2. Edit `e2e/docker-compose.e2e.yaml`: add `ipam.config.subnet` to both
   networks, switch `shepherd.networks` to the long map form with
   `ipv4_address` pins, add the two `SHEPHERD_SERVER_*_LISTEN` env vars, fix
   the healthcheck `--addr`, update the RESIDUAL RISK comment.
3. Repeat step 2 for `dev/docker-compose.dev.yaml` with the dev subnet pair.
4. `docker compose -f e2e/docker-compose.e2e.yaml --profile sim up -d` (and
   the dev equivalent) — confirm shepherd comes up healthy, confirm
   `curl localhost:18080/healthz` and `curl localhost:18090/metrics` still
   work from the host (published ports must keep working — see the
   port-publish note in §2(C)'s failure-modes paragraph).
5. Extend `internal/simsvc/compose_containment_test.go`: reshape
   `composeService.Networks` to decode the long map form, add the
   `shepherdNetworkIP` helper, add the new assertions from §4. Run
   `go test ./internal/simsvc/...` — must pass against the edited compose
   files and must fail if you temporarily revert one file's env vars (verify
   this by hand, then restore).
6. Add the `P-shepherd-*` probes to `e2e/sandbox_egress_test.go` per §4,
   reading both addresses from `docker inspect` in `BeforeAll` rather than
   hardcoding the pinned literals twice.
7. Run `make e2e-egress` end to end; confirm all probes pass. Then
   temporarily blank `SHEPHERD_SERVER_LISTEN`/`_METRICS_LISTEN` in the e2e
   compose file, re-run `make e2e-egress`, confirm `P-shepherd-deny` and
   `P-shepherd-deny-api` go red, then restore the env vars and confirm green
   again — this is the required red/green proof, not optional polish.
8. Update `project-status.md`'s B-CONTAIN-1 section to closed, with the same
   red/green evidence line style the B-STAGEORDER entry already uses (dated,
   naming the exact probes). Update the RESIDUAL RISK compose comments (done
   in steps 2-3) and cross-check
   `docs/archive/reviews/s3-sandbox-security-findings.md`'s status line for
   B-CONTAIN-1/2 if it references compose-level exposure anywhere.
9. Do NOT flip `simulator.enabled` defaults or touch F5's gate — B-CONTAIN-1
   closing is necessary but not sufficient per `project-status.md` (F5 also
   needs "NetworkPolicy enforcement verified in a real cluster", i.e.
   `kind-test-environment-plan.md` Layer B, which is separate and unbuilt).
