# Kubernetes e2e suite

The things `docker compose` structurally cannot verify: NetworkPolicy enforcement, the Helm chart
as actually deployed, and (later) Shepherd against real telemetry backends.

```bash
make e2e-k8s          # build images, create cluster, run, destroy
make e2e-k8s-clean    # remove clusters a killed run left behind
```

One cycle is ~500s for six features, including cluster create and destroy. (It was ~200s for
four before the Gateway API route-conformance and operator-owned-attachment features landed —
if you add a feature, update this number rather than leaving a stale one to mislead.)

## Isolation model

This is the part that makes the suite a base other tests can build on rather than a house of
cards. Every feature is **independently runnable** (`go test -run TestX` alone), **order
independent**, and **repeatable** (running it twice works).

| Scope | What | Why |
|---|---|---|
| Cluster, once in `TestMain` | kind cluster, Calico, CoreDNS, container images, one Postgres | `kind load` is a node-level operation; a Postgres per feature would cost ~15s each for no isolation gain |
| Per feature, `Setup`/`Teardown` | its own **namespace** and its own **database** | releases, Services and NetworkPolicies from one feature can never be seen by another, and one feature's migrations cannot satisfy another's assertions |

`newFixture` deletes before it creates, so a killed run never blocks the next one. There is
deliberately **no** environment-level namespace and no `cfg.Namespace()` helper — features take a
namespace parameter, so nothing can silently target the wrong one.

The first version shared a namespace, and the second feature to run failed instantly on a Pod that
already existed. Sharing also makes ordering matter, which is what turns a suite into a house of
cards.

## Setup and teardown

Setup order matters and each step exists because its absence produced a confusing failure:

1. **kind cluster** — `disableDefaultCNI`, pod CIDR `10.244.0.0/16`, 2 workers
2. **Pod-CIDR guard** — fails in ~40s if the pod CIDR contains the node IPs
3. **Calico** — pinned version, applied after creation
4. **Nodes Ready** — the CNI is serving
5. **CoreDNS Available** — Ready nodes do *not* imply working DNS
6. **Images loaded**, **shared Postgres**
7. **Gateway API CRDs + NGINX Gateway Fabric** — pinned from `deploy/versions.env`
8. **CloudNativePG + External Secrets operators** — pinned the same way, for the chart's two
   optional dependencies (`chart_deps_test.go`). Shepherd's chart installs neither and requires
   neither; they are here so those integrations are proven against real controllers rather than
   against `helm template`.

Teardown is layered because the framework's own hook does not cover everything:

- `testenv.Finish` — runs on success, failure and panic
- `sweepCluster` after `testenv.Run` — covers **Setup failure**, which `Finish` does *not* reach and
  which is exactly when a cluster is most likely to leak
- `make e2e-k8s-clean` — for SIGKILL, the one path no in-process hook can cover

`E2E_K8S_KEEP=1` keeps both the cluster *and* the namespaces (keeping only the cluster deletes the
evidence, which happened once).

## Two rules this suite holds itself to

**1. Prove the enforcer before trusting a denial.** `TestCNIEnforcesNetworkPolicy` must first
*connect* with no policy, then be *denied* under default-deny. If a CNI does not implement
NetworkPolicy, every deny-assertion passes for the wrong reason — so a non-enforcing CNI fails the
suite loudly rather than skipping. That phase is also the executable demonstration of what an
operator loses on Flannel.

**2. Assert the consequence, not the declaration.** After `helm install` the suite queries
`schema_migrations` in the database. `helm` exiting 0 says the hook *ran*, not that the schema
*moved*. Equally, it does **not** assert the migrate Job still exists — it is
`hook-delete-policy: hook-succeeded`, so asserting on it would fail for the one reason that means
everything worked.

## Not testing stale code

`make e2e-k8s` depends on `docker-build-local` and `docker-build-simulator`, matching every other
e2e target (`e2e`, `e2e-sim`, `e2e-egress`, `test-fullstack`). **This is the real guarantee**, and
it is correct because Docker's build cache is content-addressed: a genuine source change misses the
cache and rebuilds; an unchanged tree reuses a valid image.

The suite additionally *warns* if an image looks older than the newest source file. That is a hint
for anyone running `go test -tags e2ek8s` directly, not a guard — it compares mtimes, so a
cache-hit rebuild leaves the timestamp unchanged and the warning can be a false alarm. It was
briefly a hard failure and had to be downgraded for exactly that reason.

Images are loaded with `pullPolicy=Never`, so a missing image fails loudly instead of the cluster
silently pulling something else from a registry.

## Standard patterns used

- `sigs.k8s.io/e2e-framework` — the standalone successor to the k/k in-tree framework, which is
  coupled to the Kubernetes repo and wrong for a downstream product
- `TestMain` + `env.Environment` for cluster lifecycle; `features.New(...)` per behaviour
- `wait.For(conditions.New(...))` rather than hand-rolled polling
- kind + `kind load docker-image` rather than a registry
- Per-test namespaces, the common isolation pattern for cluster suites
- A dedicated build tag (`e2ek8s`) so this never runs alongside the compose suite by accident

## Known limits

- **Calico is pinned and assumed.** Testing a deliberately non-enforcing CNI (to prove the negative
  control from the other side) is not wired up; the CNI is currently a constant, not a parameter.
- **No LGTM layer yet.** Until it exists, "the pipeline is correct" still does not mean "the data
  arrived" — see `docs/kind-test-environment-plan.md` §5 Layer C.
- **Chart upgrade coverage installs the same version twice.** A true previous-version upgrade needs
  a released chart to upgrade *from*.
- **The dependency operators are pinned exactly, and only the current pin is tested.** A newer
  CloudNativePG or External Secrets could change a CRD field or a generated secret's key names
  without this suite noticing until the pin is bumped. That is the trade for testing a fixed,
  reproducible target; the pins and the reasoning live in `deploy/versions.env`.
