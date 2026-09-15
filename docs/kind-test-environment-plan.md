# Kubernetes test environment — plan

> Status (2026-08-22, re-checked 2026-09-11 — no code change to this suite this session; the suite
> ran green on the v0.5.0 release PR, #52, `e2e-k8s.yml` run 34614691090): **steps
> 1–2 implemented** (`e2e/k8s/`, `make e2e-k8s`); **step 3 partially done** (default-values Helm
> install, `chart_deps_test.go`'s own-dependencies check, and repeatability specs in
> `e2e/k8s/helm_install_test.go` and `helm_repeatable_test.go`; full-values install and true
> previous-version upgrade still pending — the latter's blocker is gone as of `v0.3.5`/chart 0.9.0,
> §9 item 4, but the spec itself is not written); **§5 Layer B implemented**
> (`e2e/k8s/simulator_containment_test.go`: all seven probes plus the kill probe, gated behind §4's
> CNI control — see §8b for what building it taught us about probe observability); Layer C and the
> remaining steps proposed.
>
> **Grown since**: the suite now runs **nine features** (re-counted 2026-09-11 — one
> `features.New(...)` per `TestX` function across `e2e/k8s/*_test.go`, `TestMain` excluded:
> `chart_deps_test.go`, `helm_install_test.go` (×2), `helm_repeatable_test.go`,
> `negative_control_test.go`, `route_apply_test.go`, `route_conformance_test.go`,
> `simulator_containment_test.go` (×2) — command: `grep -h '^func Test' e2e/k8s/*_test.go | grep -v
> TestMain | wc -l`), adding Gateway API route
> conformance (`route_conformance_test.go`, gates G3/G4 with a live kill probe) and operator-owned
> attachment verification (`route_apply_test.go`) since the six-feature count this line previously
> gave. The ~500s runtime figure was not re-measured this session (no Docker in this worktree) and
> likely understates nine features' worth of cluster work. Its `pull_request` paths filter also now covers
> `deploy/Dockerfile*` and `deploy/versions.env`, because the suite builds and installs
> `shepherd:local` — a base-image change that broke `alloy validate` in the shipped image reached a
> release without ever triggering this job.
> **Pins, as of 2026-09-14**: `KIND_NODE_IMAGE`, `CALICO_VERSION` and `NGF_CHART_VERSION` now live
> in `deploy/versions.env`, not as Go constants in this package — this suite reads them the same
> way it always read the other operator pins (`E2E_K8S_NODE_IMAGE` still overrides the node image
> for this suite alone), and they are shared with the new §11 reusable dev stack (`make dev-kind`),
> so a version bump is one commit reviewed once instead of two drifting copies.
> Goal: a repeatable, self-tearing-down Kubernetes environment that verifies the things
> `docker compose` structurally cannot — NetworkPolicy enforcement, the Helm chart as deployed,
> and Shepherd's behaviour against a realistic LGTM stack.

## 1. Why this exists

Kubernetes is the production target; compose is a local-development convenience. Three things
follow from that, and none of them is testable today.

**Containment is a Kubernetes control.** S3 sandbox simulation executes user-authored Alloy
config. Its containment rests on `deploy/helm/shepherd/templates/networkpolicy-simulator.yaml` —
default-deny on both `Ingress` and `Egress`, opened only to the pod's own harness ports and
kube-system DNS. Today the only thing checking that policy is `deploy/helm/chart_test.go`, which
greps `helm template` output and asserts the YAML *renders*. That is the same
asserts-the-declaration-not-the-effect pattern this repo already condemned for compose. Nothing
has ever dialled out of a simulator pod to see whether the policy does anything.

**The chart is deployed by nobody in CI.** `make helm-lint` runs `helm lint` and `helm template`.
Neither installs the chart, so a manifest that renders perfectly and fails on apply — a bad probe,
an unschedulable resource request, a missing RBAC verb — ships undetected.

**Nothing exercises Shepherd against real telemetry backends.** The compose stack has real Alloy
agents but no Prometheus, Loki or Tempo. Destinations are `example.com` URLs that nothing writes
to, so "the pipeline is correct" has never meant "data arrives".

## 2. Framework: `sigs.k8s.io/e2e-framework`

The Kubernetes project's own e2e machinery (`k8s.io/kubernetes/test/e2e/framework`) is coupled to
the k/k tree and is the wrong dependency for a downstream product. `sigs.k8s.io/e2e-framework` is
the standalone successor and is a direct fit:

- Cluster lifecycle is declarative and symmetrical, which is exactly the spin-up/spin-down
  requirement: `testenv.Setup(envfuncs.CreateCluster(...))` and
  `testenv.Finish(envfuncs.DestroyCluster(...))`, driven from `TestMain`. Teardown runs even when
  specs fail.
- `kindCluster.LoadImage(ctx, image)` puts locally-built images into the cluster with no registry.
- `utils.RunCommand` covers `helm` and `kubectl apply` without wrapping them in Go clients.
- `wait.For(conditions.New(...).DeploymentAvailable(...))` replaces hand-rolled polling.
- Plain `go test` — it composes with the existing `//go:build e2e` convention rather than
  introducing a second test runner.

It is a test-only dependency and does not enter the shipped binary.

## 3. Cluster topology

New package `e2e/k8s/`, build tag `//go:build e2ek8s` — a distinct tag from the existing `e2e`
so the compose suite and the cluster suite never run in the same invocation by accident.

### CNI: Calico, deliberately, with the default disabled

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true   # kindnetd is not a NetworkPolicy engine we want to trust
  podSubnet: "10.244.0.0/16"
nodes:
  - role: control-plane
  - role: worker           # so pod-to-pod policy crosses a real node boundary
```

kind ships `kindnetd`, whose NetworkPolicy support is not something to rest a security claim on.
Calico is installed in `Setup` after cluster creation. **The choice of CNI is itself part of what
is under test** — see §4.

A second worker matters more than it looks: with a single node, pod-to-pod traffic never leaves
the node, and a CNI can appear to enforce policy while only ever filtering loopback-ish paths.

## 4. The design principle: prove the enforcer before trusting a denial

This is the part that makes the suite worth having, and it comes straight out of what this repo
has already been bitten by.

**A "connection denied" result means nothing on its own.** If the CNI does not implement
NetworkPolicy, every deny-assertion passes for the wrong reason, and the suite reports containment
that does not exist — silent by construction, exactly like the compose `internal: true` assertions
and the self-skipping specs before them.

So the suite opens with a **negative control**, and refuses to run the rest if it fails:

1. Deploy two throwaway pods, `prober` and `target`, in a scratch namespace with no policy.
2. `prober` dials `target`. **This must SUCCEED** — it proves the prober, the image, DNS and the
   dial method all work. A deny later then means something.
3. Apply a default-deny NetworkPolicy to that namespace.
4. `prober` dials `target` again. **This must now FAIL** — it proves the CNI actually enforces
   policy.
5. Only if both hold does the suite proceed. If step 4 still succeeds, every containment spec is
   marked failed with "the CNI does not enforce NetworkPolicy — containment results from this
   cluster are meaningless", not skipped.

Step 4 is also the executable form of the documentation deliverable in §7: it is a demonstration,
in the repo, of precisely what an operator loses on a non-enforcing CNI.

## 5. What the suite tests

### Layer A — chart deploys and runs (the cheap win)

- `helm install` the chart with default values; every workload becomes Available.
- Install again with `deploy/helm/shepherd/ci/full-values.yaml` (simulator on, ingress, HPA, ServiceMonitor).
- The migrate Job completes; Shepherd's `/healthz` and `/readyz` answer through a Service.
- Upgrade path: install the previous chart version, `helm upgrade` to this one, still healthy.
  This is the only place chart upgrades are ever exercised.

### Layer B — simulator containment (the reason this exists)

Runs only after §4's negative control passes. From **inside the simulator pod**, using an ephemeral
debug container so nothing is added to the shipped image:

| Probe | Expectation | What it proves |
|---|---|---|
| `P-harness` | **reachable** | the sandbox can still reach its own capture receivers — containment did not become deny-everything |
| `P-shepherd` | denied | closes B-CONTAIN-1: the sandbox cannot scrape the control plane |
| `P-incluster` | denied | no lateral movement to an arbitrary in-cluster Service |
| `P-node` | denied | the node/host is not reachable — the Kubernetes answer to B-CONTAIN-2 |
| `P-external` | denied | no egress to the internet |
| `P-dns-only` | resolves, cannot connect | DNS is open by design; that must not imply reachability |
| `P-apiserver` | denied | plus `automountServiceAccountToken: false` means no credential to use if it were |

`P-harness` is not decoration. Every other row is a denial, and a suite of only-denials passes
perfectly when the pod has no network at all.

**Kill probe**, mirroring the compose suite's standard: delete the NetworkPolicy, re-run, and
require that `P-shepherd` and `P-external` now **succeed**. A containment suite that stays green
with the policy removed is not testing the policy.

### Layer C — real telemetry (LGTM)

A deliberately small stack in its own namespace — Prometheus (or Mimir), Loki, Tempo, Grafana —
sized for a laptop, not for load. Then:

- Create a Destination pointing at the in-cluster Prometheus, build a pipeline in the visual
  builder's model, serve it to a real Alloy agent, and **assert the series arrives in Prometheus**.
  That is the first end-to-end proof that a Shepherd-authored pipeline actually delivers data.
- The same for logs into Loki, which is where **B-STAGEORDER would have been caught by behaviour
  rather than by reading the config**: author `stage.json` then `stage.drop`, and assert the
  dropped lines are absent and the kept ones carry the extracted label.
- With the simulator enabled, run an S3 sandbox run and assert captured series come from the
  synthetic exporter and **not** from the real Prometheus — containment and function in one spec.

Layer C is where "the pipeline is correct" finally means "the data arrived".

## 6. Lifecycle, cost and CI

**Local**: `make e2e-k8s` — create cluster, install Calico, load images, install chart, run specs,
destroy cluster. `E2E_K8S_KEEP=1` leaves the cluster up for debugging, matching the existing
`E2E_KEEP` convention.

**Cluster naming** uses `envconf.RandomName` so concurrent runs and leftover clusters from a
killed run never collide. A `make e2e-k8s-clean` deletes any `shepherd-e2e-*` cluster.

**Teardown is guaranteed** by `testenv.Finish`, which runs on panic and on failure. The one case it
cannot cover is SIGKILL, hence the clean target.

**Cost is real and must be sequenced accordingly.** Cluster create plus Calico plus image load is
roughly 2–4 minutes before a single assertion runs; the LGTM layer adds more. So:

- **As proposed here**: Layers A and B run in CI on pull requests **paths-filtered** to
  `deploy/helm/**`, `internal/simsvc/**`, `internal/simulate/**` — the containment surface. This
  mirrors how `e2e-egress` is already gated. Layer C runs nightly and on `main`, not per-PR.
- **As built** (`.github/workflows/e2e-k8s.yml`): the layer split above was not carried into CI —
  the whole nine-feature suite runs as one job, gated by one `pull_request` paths filter
  (`deploy/helm/**`, `e2e/k8s/**`, the workflow file itself, and `deploy/Dockerfile*` /
  `deploy/versions.env` — the suite builds and installs `shepherd:local`, and a base-image change
  once broke `alloy validate` in the shipped image without ever triggering this job, per the note
  above) plus a **weekly** `schedule` (not nightly) and `workflow_dispatch`. `internal/simsvc/**`
  and `internal/simulate/**` are not in the filter — a change confined to those paths does not
  trigger this job today.
- Nothing here joins the default `make test`.

## 7. Documentation deliverable

A new section in the deployment docs, and a prominent note in the chart's `NOTES.txt` when
`simulator.enabled: true`:

> **Sandbox simulation requires an enforcing CNI.** S3 executes user-authored Alloy configuration.
> Its isolation is the simulator's NetworkPolicy, and a NetworkPolicy is only enforced if your CNI
> implements it. Calico, Cilium and Antrea do. **Flannel does not**, and the AWS VPC CNI requires
> its network-policy agent to be enabled. On a non-enforcing CNI the policy applies successfully
> and silently does nothing: the sandbox can then reach any pod, Service or node the simulator pod
> can route to, and anything a user's pipeline names becomes reachable. Verify enforcement before
> enabling the simulator; `make e2e-k8s` contains a probe that demonstrates the difference.

Two things make this honest rather than boilerplate: it names the CNIs, and it points at a probe
that reproduces the failure instead of asking the reader to take it on faith.

## 8. Sequencing

| Step | Deliverable | Depends on |
|---|---|---|
| 1 | ~~`e2e/k8s` skeleton: TestMain, kind config, Calico, teardown, `make e2e-k8s`~~ **done** | — |
| 2 | ~~§4 negative control + the "CNI does not enforce" hard failure~~ **done** | 1 |
| 3 | Layer A chart-deploys specs — **partially done** (default-values install + repeatability landed; full-values + upgrade pending) | 1 |
| 4 | ~~Layer B containment probes + kill probe~~ **done** (`simulator_containment_test.go`, §8b) | 2, 3 |
| 5 | §7 documentation and `NOTES.txt` warning — **not done**: `deploy/helm/shepherd/templates/NOTES.txt` carries no CNI/NetworkPolicy warning today | 4 |
| 6 | ~~CI wiring, paths-filtered~~ **done** (`.github/workflows/e2e-k8s.yml`) | 4 |
| 7 | Layer C LGTM stack and delivery assertions — **not started** | 3 |

Steps 1–4 answer the S3 containment question and are the point of the exercise. Step 7 is
independently valuable and can follow later.

## 8a. What building steps 1–2 actually taught us

Three things the plan did not anticipate, each now encoded in the harness:

- **Calico's documented kind pod CIDR is wrong on OrbStack.** The standard guidance says
  `192.168.0.0/16`, which assumes Docker's usual 172.17.x bridge. Here the kind network came up on
  `192.168.117.0/24` — *inside* the pool. Nothing errored: calico-kube-controllers crash-looped and
  CoreDNS never became Ready, surfacing five minutes later as `nc: bad address` in an unrelated
  spec. Now `10.244.0.0/16`, with `assertPodCIDRDoesNotOverlapNodes` failing in ~40s with one clear
  sentence if it ever recurs.
- **Nodes Ready does not mean DNS works.** CoreDNS is an ordinary Deployment that schedules after
  the CNI, so the first spec raced it. The first run passed only because image pulls happened to
  give it enough time — a latent flake that would have read as a policy denial. `waitForClusterDNS`
  now gates on it.
- **`testenv.Finish` does not run when Setup fails** — which is exactly when a cluster is most
  likely to leak. Two were left behind while getting the CNI right. `sweepCluster` after
  `testenv.Run` covers every path Finish misses; verified by forcing a Setup failure and
  confirming no cluster survived.

The polling in both probe phases came from the same lesson: a single dial races infrastructure and
fails for reasons unrelated to policy.

## 8b. What building Layer B actually taught us

- **`kubectl debug --attach` does not reliably propagate the debug container's exit code.** The
  first probe implementation judged connect-vs-deny by the command's error status; every deny probe
  therefore reported "reached" on a cluster whose policy provably denied the same dial (verified by
  hand: pod IP, Service IP, and FQDN all blackholed). A denial was *unobservable* — the exact
  silent-by-construction failure class §6's standard names, one layer down in the harness. The
  probes now speak through output sentinels (`PROBE-CONNECTED` / `PROBE-DENIED` printed by an
  in-container `sh -c`), because stdout does survive attach; an attempt that produces neither
  sentinel is an attach flake and counts for neither outcome.
- **A deny deadline is a convergence budget, not a per-run cost.** The retry loop exits on the
  first observed denial, so a converged cluster pays one ~5s attempt per probe regardless of the
  deadline; the deadline only burns while dials keep succeeding — a real hole, or Calico/Felix
  still programming a freshly-installed policy (measured at >17s on a loaded 3-node kind). Sizing
  it "short to keep seven denials cheap" optimized a cost that does not exist and lost the race.
- The false alarm was productive: hand-verifying the denial (before finding the harness bug)
  independently confirmed the chart's simulator NetworkPolicy denies pod-IP, ClusterIP, and FQDN
  paths once programmed — and a plain `deny-all-egress` on a scratch pod confirmed the CNI
  enforces egress at all, which §4's ingress-only negative control had never established.

## 9. Open decisions

1. **Calico vs Cilium.** Calico is the smaller, faster install and is what most managed clusters
   default to. Cilium would additionally let us assert on flow logs. Proposal: Calico, and treat
   the CNI as a variable the harness can be pointed at rather than a hardcode — a second CNI is
   then a config change, and testing a *non*-enforcing CNI to prove the §4 control works becomes
   possible. **Partly realized (2026-09-14)**: `CALICO_VERSION` is now a `deploy/versions.env`
   variable instead of a Go constant, so the *version* the harness installs is a config change
   already — swapping the CNI *family* (Calico → Cilium) is still a code change, not a value one.
2. **LGTM distribution.** Individual upstream charts, or the `grafana/lgtm-distributed` /
   `docker.io/grafana/otel-lgtm` all-in-one. The all-in-one is far quicker to stand up but less
   representative. Proposal: start with the all-in-one for Layer C, revisit if it hides anything.
3. **Does the node probe belong here at all?** `P-node` is the Kubernetes analogue of
   B-CONTAIN-2, but node reachability depends on CNI and cloud provider. It may be honest to
   assert it on kind and document it as environment-dependent elsewhere, rather than imply a
   universal guarantee.
4. **Chart upgrade coverage** needs a previous version to upgrade from. **No longer blocked**:
   `v0.3.5` (tagged 2026-08-27) is a released chart version 0.9.0
   (`git show v0.3.5:deploy/helm/shepherd/Chart.yaml`), ahead of the unreleased 0.10.0 on this
   branch — step 3's true previous-version upgrade spec has something to install first now. Still
   not written (see the status header and §8 step 3); this only removes the reason it was deferred.

## 10. What this does not do

It does not make S3 containment provable on Docker Desktop or OrbStack; those remain
local-development environments where `internal: true` does not deny the host, and that stays
documented as a limitation rather than fixed. It does not test at scale — no load, no soak, no
multi-node failover. And it does not replace the compose e2e suite, which is faster and still the
right place for agent-protocol and GitOps coverage.

## 11. Reusable dev stack (`make dev-kind`)

`make dev-kind` brings up a single-node kind cluster named `shepherd-dev` running the real Helm
chart — CloudNativePG, Gateway API + NGINX Gateway Fabric, Calico, the existing dev seed, three
Alloy agents (`dev/*.alloy`), Gitea, and the navikt mock OIDC provider — everything reachable on
host port 80 under `*.localtest.me`. It is the **Kubernetes flavour of `make dev`**, not a second
copy of this suite: it exists so a developer or reviewer can see the chart, the routing and the
containment policy behave the way they will in production, without standing up the isolation and
teardown machinery a test suite needs. `scripts/dev-kind.sh` (verbs `up`, `reload`, `seed`,
`status`, `down`) is driven through five Makefile targets, `dev-kind[-reload|-seed|-status|-down]`
— see `docs/dev-guide.md`'s "Kubernetes flavour" section for the day-to-day walkthrough. The
lettered/numbered decisions cited below (D1-D4, C1-C10) are this feature's design record,
`docs/plans/2026-09-14-kind-dev-stack.md`.

### What it shares with this suite, and what it deliberately does not

**Shared:** the CNI, the router and the operator, at the exact pinned versions — `KIND_NODE_IMAGE`,
`CALICO_VERSION` and `NGF_CHART_VERSION` moved into `deploy/versions.env` for this reason (D2,
the status header above). The dev cluster's Calico is the same enforcing CNI §4 proves, so the
chart's default-deny simulator NetworkPolicy is real there too, matching how it behaves in
production and in this suite — not a compose approximation.

**Deliberately not shared:**

- **One node, not this suite's multi-node topology (§3).** Dev gains nothing from proving policy
  crosses a node boundary; it only needs Calico's NetworkPolicy engine live.
- **No per-feature namespaces.** This suite gives every feature its own scratch namespace and
  database so features never interfere (`e2e/k8s/README.md`'s isolation model). The dev cluster has
  exactly one namespace, `shepherd-dev`, for the life of the cluster — there is only one "feature"
  running, the whole stack, for as long as a developer wants it up.
- **No teardown on failure.** `testenv.Finish` tears this suite's cluster down on every path,
  including panic, because a leaked e2e cluster is pure waste. The dev cluster is meant to persist
  across a working session; `down` is a separate, explicit verb (`make dev-kind-down` = `kind
  delete cluster --name shepherd-dev`) — the `dev-reset` equivalent for the Kubernetes flavour, and
  local-path PVs (CNPG's data, Gitea's PVC) die with it, same as compose's named volumes.
- **Fixed cluster name.** `shepherd-dev`, not this suite's `envconf.RandomName`-generated
  `shepherd-e2e-*` names, so `make e2e-k8s-clean` (which only sweeps `shepherd-e2e-*`) never
  touches it and the two stacks can coexist on one laptop.

### Routing and DNS: one Gateway, one CoreDNS rewrite

The dev stack routes everything through Gateway API + NGINX Gateway Fabric (D3), the same
controller this suite proves conformance against (`route_conformance_test.go`) — on port 80, under
`*.localtest.me`, with an `HTTPRoute` per service: `shepherd.localtest.me` (the chart's own route),
`oidc.localtest.me` and `gitea.localtest.me` (hand-written, `dev/kind/routes.yaml`). NGF is
installed with `nginx.service.type=NodePort` and a single NodePort (`30080` → listener port `80`);
kind's `cluster.yaml` maps host port 80 to that NodePort, so a browser on the host and a pod inside
the cluster both land on the same Gateway.

DNS is **one rewrite for the whole zone**, not a line per hostname: the script inserts
`rewrite name regex (.*)\.localtest\.me <data-plane-svc>.shepherd-dev.svc.cluster.local answer
auto` into CoreDNS's Corefile and restarts it. Two things make one rewrite the right shape rather
than a shortcut:

- **A new `*.localtest.me` route needs no DNS change** — the regex already covers it, because
  every hostname under the zone resolves to the same Gateway Service; only the `HTTPRoute`'s
  hostname list decides what actually answers.
- **The rewrite exists so in-cluster clients can resolve the zone at all**, not to keep two paths
  in sync. `oidc.localtest.me`'s public wildcard answers `127.0.0.1` — inside a pod that is the
  pod's own loopback, not the Gateway — so without the CoreDNS rewrite, Shepherd's own OIDC
  discovery of the chart-declared issuer (D4) would fail to resolve it. With the rewrite, a pod
  resolves `oidc.localtest.me` to the data-plane Service and reaches the same Gateway a browser
  reaches by the host's public-DNS-then-hostPort path — both arrive with `Host:
  oidc.localtest.me` on port 80, and the mock OIDC provider derives its issuer from that `Host`
  header, so the pod-seen and browser-seen issuer strings match. (§5 step 7's live curl from a pod
  is what actually proves the pod-seen string.)

Offline (no public DNS to `localtest.me`'s wildcard `127.0.0.1` A record): add
`shepherd.localtest.me`, `oidc.localtest.me` and `gitea.localtest.me` to `/etc/hosts` pointing at
`127.0.0.1` — CoreDNS's rewrite only affects resolution *inside* the cluster; the host still needs
its own path to `127.0.0.1:80`.

### The OIDC walk, and the `dialGuard` boundary

The chart declares the OIDC provider directly in `dev/kind/values.yaml` (`config.oidc.*`) —
D4's **values-declared issuer only**. That makes the SSO settings page (`/admin/auth`) read-only
against this stack by design: `config.go`'s non-empty `oidc.issuer` makes `HelmManaged()` true,
`settings_admin.go`'s `StatusMessage` returns *"This provider is configured by the Helm chart
(oidc.issuer). Change it in your chart values; it cannot be edited here."*, and the admin UI
disables the form, Save and Test connection accordingly.

This is not an oversight to route around — it is `internal/auth/discovery.go`'s `dialGuard`
working as intended, behind a first barrier that fires earlier. Were the chart issuer not already
set, an admin using the SSO settings page (`/admin/auth`) and pressing **Test connection** —
`Handler.TestSettings` (`settings_admin.go`) — hits `validateIssuer` (`settings.go`) before any
network call is made: it rejects every non-https issuer with *"issuer URL must use https — the
discovery document names the JWKS endpoint Shepherd fetches signing keys from, and over http
anyone on the path can substitute their own"*. So pasting `http://oidc.localtest.me/default`
never reaches the network — it is refused right there.

Paste `https://oidc.localtest.me/default` instead (the scheme this stack does not actually serve,
but the one that clears `validateIssuer`) and the probe reaches `fetchDiscovery`, which runs with
the guarded client (`SourceDatabase`) and dials out. `dialGuard` is a `net.Dialer.Control` hook
that runs on every hop of every redirect chain and refuses to connect anywhere that resolves to a
loopback, private, link-local, or carrier-grade-NAT address (`blockedIP`, `discovery.go`) —
because a real identity provider is never reachable only from inside the network making the
request. The mock OIDC provider resolves to the NGF Gateway's in-cluster ClusterIP, which is
exactly such an address, so the admin gets *"refusing to fetch
https://oidc.localtest.me/default/.well-known/openid-configuration: it resolves to a private or
loopback address, which an identity provider never does"* (`discovery.go`). Both refusals are the
product working correctly, not a dev-stack limitation to fix — https first, then the dial guard.

(Plan §5 step 10 walks the same scenario live; correct it there too before the walk, so the live
run confirms the same two-step refusal instead of the wrong one.)

### What the first live bring-up found (2026-09-15)

Three things no static check could show, each fixed on the same branch with a red run:

- **The chart's own HTTPRoute never resolved through NGINX Gateway Fabric.** The Service hardcoded
  `appProtocol: kubernetes.io/h2c`, and NGF refuses to proxy an HTTP route to an h2c upstream
  (`ResolvedRefs=False / UnsupportedProtocol`; every request answered 500 from nginx). The
  conformance suite routes to the receiver tier, not to Shepherd's Service, which is why it had
  never been seen. `service.appProtocol` is now a chart value; the h2c default is unchanged and
  `dev/kind/values.yaml` clears it.
- **A chart-declared private issuer was blocked at the key fetch.** Discovery used the per-source
  client, but the go-oidc Provider built from the document — and its JWKS fetches — used the
  guarded client regardless of source, so the first SSO login failed after the code exchange with
  *"fetching keys ... address is not a public internet address"*. The provider and the code exchange
  now follow the issuer's source (`internal/auth/discovery.go`, `auth.go`). The `dialGuard`
  boundary above still holds for admin-supplied issuers.
- **The Alloy agents were applied before the chart.** Alloy's `remotecfg` exits when the `shepherd`
  Service does not resolve on its initial load, so the three agents crash-looped through five
  restarts each until the backoff lined up with the Service appearing. `cmd_up` now applies them
  after `install_shepherd`.

One observation, not a dev-stack bug: a 30-second sandbox run against a pipeline with a
30-second scrape interval captures either one scrape or none, depending on where the scrape
jitter lands — the first run showed 0 series and the next two showed 21. The simulator's
containment and capture work under Calico; the run window versus the scrape interval is a product
question for the ledger.

The walk itself (full detail and the exact claims JSON for each persona in
`docs/plans/2026-09-14-kind-dev-stack.md` §5 step 10): the login page offers both the local form
and an SSO button labelled "Mock SSO". Signing in through it lands on
`http://oidc.localtest.me/default/authorize?…`, mock-oauth2-server's interactive login page (a
**Username** field and a **Claims** JSON textarea — `interactiveLogin: true`). A `groups` claim
naming the seed's app-admin group id produces an app admin with the Admin menu; a `groups` claim
naming one of the seed's org groups produces the matching org role with no Admin menu — the
`generic` provider preset (C7) uses `sub` as the subject and reads groups from the claim directly,
no Microsoft Graph call.

### `reload` mechanics: a build annotation, not a bare rollout restart

`make dev-kind-reload` rebuilds `shepherd:local`, `kind load`s both `shepherd:local` and
`shepherd-simulator:local` into the cluster, and runs `helm upgrade --install` again with a fresh
`--set-string podAnnotations.dev-build=<short-sha>-<unix-ts>` (C3).
A bare `kubectl rollout restart` was considered and rejected: the image tag never changes
(`pullPolicy: Never`, tag `local`), so Helm's rendered manifest is otherwise identical between
reloads — an unchanged render means no Deployment update and no rollout — and the chart's migrate
Job runs as a pre-install/pre-upgrade Helm hook — a `rollout restart` bypasses Helm entirely and
would replace pods with a new binary that never ran a pending migration against them. Setting a
value that changes every reload forces the Deployment's `dev-build` template annotation to differ
on every render, which both makes Helm roll the pods *and* keeps the migrate hook — which only
runs on `helm upgrade`, not on a raw `kubectl` restart — in the path every time.

### Where the manifests live, and why

The manifests live in **`dev/kind/`**, beside `dev/docker-compose.dev.yaml` and
`dev/shepherd.dev.env` — the files they mount as ConfigMaps and a Secret — not under `deploy/`,
which stays production-only artefacts (C9). `deploy/` is what `make config-scan`'s Trivy
misconfiguration scan covers at CRITICAL/HIGH (`scan-ref: deploy`); Gitea and mock-oauth2-server
are not designed for a read-only root filesystem the way the chart's own workloads are, and adding
their findings to `.trivyignore` would silence those checks for the chart too. Keeping the dev-only
manifests out of `deploy/` keeps that gate meaningful rather than widening it. For the same reason
the Gitea and mock-oauth2-server image pins live in `dev/kind/gitea.yaml` and `dev/kind/oidc.yaml`
themselves, not `deploy/versions.env` — every `versions.env` change bills this suite, `e2e-sim` and
`schema-verify` on the PR that makes it (D2), and neither image is part of what those suites
verify.

| File | Contents |
|---|---|
| `dev/kind/cluster.yaml` | kind cluster config: one node, `disableDefaultCNI`, the host-port mapping |
| `dev/kind/values.yaml` | Helm values for the `shepherd` release |
| `dev/kind/gateway.yaml` | the `dev` Gateway |
| `dev/kind/routes.yaml` | the `oidc` and `gitea` `HTTPRoute`s |
| `dev/kind/oidc.yaml` | the mock OIDC Deployment/Service |
| `dev/kind/gitea.yaml` | the Gitea Deployment/Service/PVC |
| `dev/kind/alloy.yaml` | the three Alloy agent Deployments |

### Agents do not run kind

**No coding agent creates, uses or deletes a `shepherd-dev` (or any) kind cluster, and none runs
`make dev-kind*` or `make e2e-k8s`, as part of an automated slice of work.** Only one
`shepherd-dev` cluster can exist at a time (its fixed name and host-port mapping make it exclusive;
kind itself supports many clusters, and this suite's own `shepherd-e2e-*` clusters coexist with it
for exactly that reason, per the section above) and only one process can bind host port 80;
several people and several agent sessions share these laptops. Every change to the manifests, the
script or the Makefile targets is verified statically instead — `helm template`/`helm lint`
against the new values, `kubectl create
--dry-run=client --validate=false` against the manifests, `bash -n`/`shellcheck` against the
script, and repocheck specs parsing everything with `yaml.v3` rather than applying it. A human, or
an orchestrating session acting on a human's explicit instruction, performs the one live bring-up
that exercises the whole stack together.
