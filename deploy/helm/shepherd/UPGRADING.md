# Upgrading the Shepherd chart

## 0.15.x → 0.16.0

Nothing to do unless you run the receiver tier (`receiver.enabled: true`).

**With the receiver on, Shepherd now applies tenant routes itself**
(`receiver.applyTenantRoutes`, default `true`). The chart grants Shepherd's
ServiceAccount a Role on `httproutes` in the release namespace and a ClusterRole
that can only `get` the `httproutes.gateway.networking.k8s.io` CRD, and the
Shepherd pod now mounts its service-account token. With the receiver off,
none of this renders.

**If you wrote tenant HTTPRoutes by hand** (as the 0.15 docs told you to),
Shepherd will create its own for the same paths, named
`shepherd-tenant-route-<route id>`. It never touches HTTPRoutes it did not create,
so after upgrading, check that each tenant route shows `applied` and then delete
your hand-written copies. Or keep writing them yourself and set
`receiver.applyTenantRoutes: false`.

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
