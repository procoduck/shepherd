# Upgrading the Shepherd chart

## 0.18.x → 0.19.0

An ordinary `helm upgrade`, then **one command to convert wizard pipelines
generated before destination auth (#229)**.

### What changed

Wizards now write a destination's URL and auth (`basic_secret` /
`oauth2_secret`, read from the Kubernetes Secret on each spoke cluster) into
the pipeline they generate. Pipelines generated earlier still carry the old
writer, `url = sys.env("SHEPHERD_DEST_<NAME>_URL")` with no auth: nothing sets
that variable, so unless you set it on your collectors yourself, those writers
have never shipped anything. From this release, saving a destination also
regenerates every wizard pipeline that uses it.

### What to do

After the upgrade, from a server pod (it needs the server's configuration —
database URL and Alloy binary), list what would change:

```sh
kubectl exec svc/<release> -- /usr/local/bin/shepherd admin rerender-destinations \
  --config /etc/shepherd/shepherd.yaml --dry-run
```

Then run it without `--dry-run`. For each organisation it regenerates every
wizard pipeline still carrying the `sys.env(...)` writer from its stored
wizard answers and the organisation's current destinations, validates the
result like any pipeline edit (Stages 1–3), and stores it with a new revision
and a `pipeline.rerender` audit row (actor `system:rerender-destinations`).
Pipelines it cannot regenerate — most often one naming a destination that has
since been deleted — are listed, each with what to do, and left unchanged; fix
them (create the destination again, or on the pipeline's page detach it from
the wizard or delete it) and run the command again. It is safe to repeat.

**Expect a reload:** every collector served one of the converted pipelines
receives a new config on its next poll and reloads once. If you had set
`SHEPHERD_DEST_<NAME>_URL` on your collectors as a workaround, check that each
destination's URL in Shepherd is the full push URL (`…/api/v1/push`,
`…/loki/api/v1/push`) before running it.

Nothing runs this at startup: it changes what the fleet is served, so it is
your step to take.

**The same command repairs three wizard templates.** Before this release some
wizard output passed validation but was refused by a running Alloy when it
loaded the config: App Observability with log collection on (`stage.logfmt {}`
/ `stage.json {}`, plus a `loki.process` stage nothing fed), Pod Logs with the
`logfmt` or `json` format, and every Blackbox pipeline (no module `config`). A
collector served one of these refuses the config with a `Failed to build
component` error. Every pipeline generated before this release carries the
`sys.env(...)` writer, so `rerender-destinations` regenerates it with the fixed
templates too — there is no separate step. If you had hand-fixed one of these
pipelines in the editor, the regeneration replaces your edit with the wizard's
output (a `level` label for logfmt/json, the module's definition for Blackbox);
your version stays in the pipeline's revisions if you want to restore it.

### Kubernetes 1.32 or newer

The chart's `kubeVersion` floor moves from 1.29 to **1.32**, the lowest version
its Kubernetes test suite proves. On a 1.29–1.31 cluster `helm upgrade` stops
with a `kubeVersion` error before changing anything; upgrade the cluster first
(all three are end-of-life upstream), or stay on chart 0.18.x.

### Database Metrics pipelines

The Database Metrics wizard now has the collector read the connection string
from a Kubernetes Secret (`remote.kubernetes.secret`; key `dsn` for PostgreSQL
and MySQL, key `password` plus a plain address for Redis). Earlier versions
rendered `sys.env("<VAR>")` and said Shepherd would set the variable — nothing
did, and on a collector without it the whole config is refused
(`cannot parse DSN`). Existing pipelines keep rendering exactly as before (so a
collector where you did set the variable keeps working);
`rerender-destinations` does not touch them. To move one to a Secret:

1. On every cluster the pipeline matches, create a Secret holding the DSN
   (key `dsn`) or, for Redis, the password (key `password` — a Redis Secret
   without that key makes the collector connect with no password).
2. Give the collector's ServiceAccount `get` on Secrets in that namespace
   (Alloy GETs the one Secret at load and every minute; it never lists or
   watches).
3. Re-run the pipeline's Database Metrics wizard, set **Credential source** to
   `kubernetes_secret`, enter the Secret's namespace, name and key (and the
   Redis address), and commit.

Only collectors running in Kubernetes can read the Secret: a collector on a
host or VM that the pipeline matches would refuse its whole config. Keep such a
pipeline's matchers to Kubernetes clusters, or leave those collectors on
`env` (set the variable on them yourself).

A collector that is refusing its config recovers on its next poll after the
commit, or as soon as you disable the pipeline.

## 0.17.x → 0.18.0

An ordinary `helm upgrade` with no values to change, but **the Shepherd Service
has no endpoints for a few seconds while the upgrade rolls** unless you pre-label
the running pods first (below).

### What changed

Every pod this chart runs — the Shepherd server, the sandbox simulator, the
receiver, the migration Job — carries the same `app.kubernetes.io/name` and
`app.kubernetes.io/instance` labels, and the objects meant for the server alone
selected on just that pair. So the simulator (and, with the receiver on, the
receiver) pod sat in the `shepherd` and `shepherd-metrics` Services'
EndpointSlices, `kubectl port-forward|exec svc/shepherd` could pick it, the
PodDisruptionBudget counted it, and with `networkPolicy.enabled: true` the app
NetworkPolicy — whose egress allows everything — applied to the simulator too,
opening the sandbox's otherwise default-deny egress.

The server pods now carry `app.kubernetes.io/component: server`, and these
select on it:

- the `<release>` and `<release>-metrics` Services;
- the PodDisruptionBudget (rendered with two or more replicas);
- the app NetworkPolicy (`networkPolicy.enabled`);
- the simulator NetworkPolicy's ingress rule (only the server may reach the
  simulator's control port).

The Deployment's own `spec.selector` is **unchanged**: Kubernetes forbids
changing it on a live Deployment, so changing it would make this upgrade fail
outright. It still matches the simulator pods, so reach a server pod through
the Service (`kubectl exec svc/<release> -- …`), not `deploy/<release>`.

### The no-endpoints window

Helm applies the new Service selector and the new pod template together. The
running pods were created from the old template and lack the new label, so from
that moment until the first new pod is Ready no pod matches: the UI, the API and
collectors' polls through the Service get no answer. With the default rolling
update (`maxSurge: 1`, `maxUnavailable: 0`) that is as long as one new pod takes
to start and pass its readiness probe — typically a few seconds. Collectors
keep running their last config and pick up on their next poll; a sandbox run
the old pods were submitting may fail, because the simulator's ingress rule no
longer admits them. For the rest of the roll only the new pods serve, so
capacity ramps up from one pod to `replicas`.

**To avoid it,** label the running server pods just before the upgrade, so the
new selector already matches them. The server pods are the release's only pods
without a component label:

```sh
kubectl -n <namespace> label pod \
  -l 'app.kubernetes.io/instance=<release>,app.kubernetes.io/name=shepherd,!app.kubernetes.io/component' \
  app.kubernetes.io/component=server
helm upgrade <release> oci://ghcr.io/procoduck/charts/shepherd --version 0.18.0 …
```

(`app.kubernetes.io/name` is your `nameOverride` if you set one.) The label does
not change which ReplicaSet owns the pods, and the roll then replaces them as
usual. A pod recreated between the two commands will not have it, so run them
together. Otherwise, upgrade in a maintenance window.

## 0.15.x → 0.16.0

An ordinary `helm upgrade`, with nothing to do: the new receiver tier is off
(`receiver.enabled: false`), and with it off the chart renders exactly the
objects it did before.

**When you turn the receiver on,** Shepherd also applies tenant routes itself
(`receiver.applyTenantRoutes`, default `true`). The chart then grants Shepherd's
ServiceAccount a Role on `httproutes` in the release namespace and a ClusterRole
that can only `get` the `httproutes.gateway.networking.k8s.io` CRD, and the
Shepherd pod mounts its service-account token. With the receiver off, none of
this renders.

**If you already created HTTPRoutes for your tenant routes yourself,** Shepherd
creates its own for the same paths, named `shepherd-tenant-route-<route id>`. It
never touches HTTPRoutes it did not create, so once each tenant route shows
`applied` on the Tenant routes page, delete your copies. Or keep managing them
yourself and set `receiver.applyTenantRoutes: false`.

## 0.9.x → 0.10.0

An ordinary `helm upgrade`. Every pod rolls once — read on for why — but there
is no manual adoption step like 0.9.0's.

### What changed

**The simulator's control API now requires a bearer token.** Before this
release `POST /v1/runs` on the sandbox simulator was reachable by anything
that could route to the Pod. Every install now authenticates it, sourced with
the same precedence Shepherd's other bootstrap secrets use, highest first:

1. `simulator.token.existingSecret` — bring your own Secret, unmanaged by
   this chart.
2. `externalSecrets.enabled` — a Password generator + `ExternalSecret` this
   chart renders (the same pattern the runtime bootstrap secrets already
   use).
3. Otherwise the chart generates its own Secret and reuses it (`lookup`) on
   every later `helm upgrade`, the same way the runtime Secret already does.

Both the `shepherd` and `shepherd-simulator` Deployments carry a new
`checksum/simulator-token` annotation, so **this upgrade rolls both pods once**
even if nothing else about your values changed. If you set `config.simulator`
explicitly (pointing Shepherd at a simulator this chart does not manage), you
must now also set `config.simulator.token` — the chart refuses to render
otherwise, naming the missing field.

**GitOps caveat.** The generated-Secret path (case 3 above) uses the same
`lookup`-reuse trick as the runtime bootstrap Secret, and the same limits
apply: Argo CD, Flux, and `helm template | kubectl apply` render with no live
cluster connection, so `lookup` returns empty and the chart regenerates the
token — and rolls both Deployments — on every sync. Set
`simulator.token.existingSecret` (or disable the simulator) to avoid that
under those tools, exactly as `UPGRADING.md`'s 0.9.0 section already
recommends for `cnpg.render` / `externalSecrets.render`.

**The sandbox's `NetworkPolicy` no longer allows DNS egress.** Cluster DNS
used to be open so the simulator could resolve the harness endpoints it talks
to; those are now dialled by loopback IP directly (D10), so the sandboxed
Alloy process has no legitimate reason to resolve a name at all. A
user-authored pipeline that names a destination by hostname inside the
sandbox will now fail closed at the network layer rather than resolving one.

**Service accounts now have a role tier**, independent of their write
capability. Every existing service account is backfilled to `editor` by the
migration; `admin` is never assigned implicitly — only a `CreateServiceAccount`
call that asks for it explicitly gets one. This is a deliberate narrowing:
an apply-capability service account that previously reached
`RoleOrgAdmin`-tier procedures (`RotateTenantRoute`, `DeleteTeam`,
`AddTeamMember`, `DeleteCredential`, `ListAudit`, `ListTeamMembers`, and
others) only because its org matched — with no tier check at all — now gets
`PermissionDenied` on those calls unless it is re-created with the `admin`
role. Check what your automation's service accounts actually call before
upgrading; a token that broke here needs `admin`, not a workaround.

**OIDC sessions now end at the ID token's expiry**, even if the session's own
sliding TTL has not run out. Entra/Okta typically mint an ID token good for
about an hour, and this deployment stores no refresh token to silently renew
it, so an OIDC user who has been idle may now be asked to sign in again
sooner than before. Local sessions are unaffected.

**Local sign-in is now throttled**, per login name and per source IP
independently. A burst of mistyped passwords still succeeds; sustained
guessing gets `429` with `Retry-After`. State is in-process and per-replica,
so the effective ceiling scales with replica count — a generous floor, not an
exact one.

**GitOps-synced files are now fully validated before they reach a pipeline.**
Previously `gitsync` only ran Stage 1 (syntax) on a synced file; a file that
passed syntax but would fail `alloy validate` (Stage 2) or the merge-time
Stage 3 checks against the collector's other pipelines could still land. All
three stages now run, and a `Reconciler` built without a validator refuses to
sync at all rather than falling back to the weaker check. If your GitOps repo
carries a pipeline that only ever passed Stage 1, this upgrade will start
rejecting it — check the reconciler's logs, not the running config, for what
changed.

### Other changes worth knowing

- Existing installs need no values changes for any of the above; every new
  behaviour activates from the values you already have. The token precedence,
  the role backfill, and the validation stages all have zero-config defaults
  that preserve what a default 0.9.x install was already doing, except where
  a gap is being closed (the simulator token, the GitOps validation depth).

## 0.8.x → 0.9.0

This release needs **one manual step before the first upgrade**, and only that
first one. Everything after it is an ordinary `helm upgrade`.

### What changed

Up to 0.8.x the runtime ConfigMap, Secret and ServiceAccount — the objects the
Deployment actually mounts — were rendered as Helm **hooks**. That was done to
solve a real ordering problem (Helm runs every hook before any normal resource,
so the pre-install migration Job could not otherwise see them), but Helm does
not track hook resources in the release, and three things followed from that:

- **`helm rollback` did not revert your config.** Rollback runs `pre-rollback`
  hooks, never `pre-upgrade` ones, so the ConfigMap stayed at the new content
  while the Deployment went back to the old image. Config/code mismatch, after
  the exact operation people run when an upgrade has gone wrong.
- **`helm uninstall` left objects behind** — including, when you used the
  `secrets` value, a Secret holding your database URL and encryption key.
- **Setting `migrations.job.enabled: false` broke the upgrade**, because the
  same objects would become tracked resources Helm could not adopt.

In 0.9.0 the migration Job gets its own short-lived copies (`<release>-migrate`,
deleted when the migration succeeds) and the runtime objects are ordinary
tracked resources.

### The manual step

Your existing ConfigMap, Secret and ServiceAccount were created as hooks, so
they carry no Helm ownership metadata and the upgrade cannot adopt them. Hand
them to Helm once. This changes no data, restarts nothing, and is safe to run
while Shepherd is serving:

```sh
REL=shepherd          # your release name
NS=shepherd           # your namespace

for obj in configmap/$REL secret/$REL-secrets serviceaccount/$REL; do
  kubectl get $obj -n $NS >/dev/null 2>&1 || continue
  kubectl label   $obj -n $NS app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate $obj -n $NS \
    meta.helm.sh/release-name=$REL \
    meta.helm.sh/release-namespace=$NS --overwrite
done
```

(`secret/$REL-secrets` exists only if you used the `secrets` value. The loop
skips whatever is not there.)

Then upgrade normally. If you forget, the chart stops with a message naming the
exact objects and repeating these commands — it does not let Helm fail with its
own "invalid ownership metadata" error.

### If you deploy with Argo CD, Flux, or `helm template | kubectl apply`

Two new values matter to you: **`cnpg.render`** and **`externalSecrets.render`**.

Both the CloudNativePG `Cluster` and the bootstrap `ExternalSecret` are
protected from being recreated by a `lookup` guard, and `lookup` returns empty
whenever the chart is rendered without a cluster connection — which is exactly
what these tools do. The guard therefore fails **open** there: the resources are
emitted on every sync, and Argo CD maps `helm.sh/hook: pre-install,pre-upgrade`
onto a PreSync hook it deletes before recreating. For the database that means
the PVCs go with it; for the ExternalSecret it means an encryption key that
cannot be rotated is silently replaced.

Once the database and the secret exist, set:

```yaml
cnpg:
  render: never
externalSecrets:
  render: never
```

`auto` (the default) keeps the 0.8.x behaviour and is correct under the helm
CLI. `always` emits them unconditionally, for bootstrapping.

### Other changes worth knowing

- `values.schema.json` now covers every value block and rejects unknown keys.
  A values file with a typo (`simulater:`, `replicaCount:`,
  `config.tracing.endpont:`) that was silently ignored before will now fail the
  install with the offending path named. That is the point, but it means a
  values file carrying old cruft may need a clean-up before it installs.
- `autoscaling.enabled` now **requires** `resources.requests.cpu`. A CPU
  utilization HPA is a percentage of the request; with no request it could
  never scale, and reported `FailedGetResourceMetric` forever instead.
- The PodDisruptionBudget now follows `autoscaling.minReplicas` when an HPA owns
  the replica count, instead of `replicas` (which the Deployment stops rendering
  in that case). If you ran `autoscaling.minReplicas: 1`, you had a budget that
  deadlocked node drains; you now correctly get none.
- New `serviceAccount.create` / `.name` / `.annotations`, for IRSA and Workload
  Identity. Defaults reproduce the old behaviour exactly.
