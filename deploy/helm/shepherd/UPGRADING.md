# Upgrading the Shepherd chart

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
