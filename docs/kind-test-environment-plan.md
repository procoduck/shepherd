# Kubernetes test environment — plan

> Status (2026-08-22): **steps 1–2 implemented** (`e2e/k8s/`, `make e2e-k8s`); **step 3 partially
> done** (default-values Helm install + repeatability specs in `e2e/k8s/helm_install_test.go` and
> `helm_repeatable_test.go`; full-values install and true previous-version upgrade still pending);
> **§5 Layer B implemented** (`e2e/k8s/simulator_containment_test.go`: all seven probes plus the
> kill probe, gated behind §4's CNI control — see §8b for what building it taught us about probe
> observability); Layer C and the remaining steps proposed.
>
> **Grown since**: the suite now runs **six features in ~500s**, adding Gateway API route
> conformance (`route_conformance_test.go`, gates G3/G4 with a live kill probe) and operator-owned
> attachment verification (`route_apply_test.go`). Its `pull_request` paths filter also now covers
> `deploy/Dockerfile*` and `deploy/versions.env`, because the suite builds and installs
> `shepherd:local` — a base-image change that broke `alloy validate` in the shipped image reached a
> release without ever triggering this job.
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
  podSubnet: "192.168.0.0/16"
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
- Install again with `ci/full-values.yaml` (simulator on, ingress, HPA, ServiceMonitor).
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

- Layers A and B run in CI on pull requests **paths-filtered** to `deploy/helm/**`,
  `internal/simsvc/**`, `internal/simulate/**` — the containment surface. This mirrors how
  `e2e-egress` is already gated.
- Layer C runs nightly and on `main`, not per-PR.
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
| 4 | Layer B containment probes + kill probe | 2, 3 |
| 5 | §7 documentation and `NOTES.txt` warning | 4 |
| 6 | CI wiring, paths-filtered | 4 |
| 7 | Layer C LGTM stack and delivery assertions | 3 |

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
   possible.
2. **LGTM distribution.** Individual upstream charts, or the `grafana/lgtm-distributed` /
   `docker.io/grafana/otel-lgtm` all-in-one. The all-in-one is far quicker to stand up but less
   representative. Proposal: start with the all-in-one for Layer C, revisit if it hides anything.
3. **Does the node probe belong here at all?** `P-node` is the Kubernetes analogue of
   B-CONTAIN-2, but node reachability depends on CNI and cloud provider. It may be honest to
   assert it on kind and document it as environment-dependent elsewhere, rather than imply a
   universal guarantee.
4. **Chart upgrade coverage** needs a previous version to upgrade from. Until there is a released
   chart version, step 3's upgrade spec has nothing to install first, and should be deferred
   rather than faked.

## 10. What this does not do

It does not make S3 containment provable on Docker Desktop or OrbStack; those remain
local-development environments where `internal: true` does not deny the host, and that stays
documented as a limitation rather than fixed. It does not test at scale — no load, no soak, no
multi-node failover. And it does not replace the compose e2e suite, which is faster and still the
right place for agent-protocol and GitOps coverage.
