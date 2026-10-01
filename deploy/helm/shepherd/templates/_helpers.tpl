{{/*
Expand the name of the chart.
*/}}
{{- define "shepherd.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shepherd.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "shepherd.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "shepherd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
The release-wide labels every pod of this chart carries: the app server, the
simulator, the receiver and the migrate Job alike. Not a selector for "the
app" on its own -- it matches all of them (#234). The only object that may
still select by exactly this pair is the app Deployment's spec.selector, which
is immutable (see deployment.yaml); everything that means "the Shepherd
server pods" uses shepherd.serverSelectorLabels below.
*/}}
{{- define "shepherd.selectorLabels" -}}
app.kubernetes.io/name: {{ include "shepherd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The Shepherd SERVER pods only (#234). The simulator and receiver pods carry
shepherd.selectorLabels plus their own component label, so a selector of
shepherd.selectorLabels alone also matched them: both app Services'
EndpointSlices listed the simulator pod, `kubectl exec|logs|port-forward
svc/shepherd` could land on it, the app PDB counted it, and the app
NetworkPolicy (allow-all egress) applied to the sandbox too. The server's own
component label makes every app-only selector exclusive.
*/}}
{{- define "shepherd.serverSelectorLabels" -}}
{{ include "shepherd.selectorLabels" . }}
app.kubernetes.io/component: server
{{- end }}

{{/*
The ServiceAccount the Shepherd workloads run as.

Exposed rather than hardcoded to fullname because the two ways of running
Shepherd against a managed database -- EKS with IRSA, GKE with Workload
Identity -- both work by annotating a ServiceAccount, and both are the database
path this chart's own values documentation recommends. With the name hardcoded
there was nowhere to put the annotation and no way to point at an account the
cluster's platform team had already created.
*/}}
{{/*
Whether the chart creates the ServiceAccount. Returns "true" or "".

Not `.create | default true`: Go's `default` fires on any EMPTY value, and
`false` is empty -- so `create: false` would have been read as "unset, so
default to true", silently creating the account the operator explicitly asked
the chart not to manage. Checked with hasKey instead, which distinguishes
"absent" from "set to false".
*/}}
{{- define "shepherd.serviceAccountCreate" -}}
{{- $sa := .Values.serviceAccount | default dict -}}
{{- if hasKey $sa "create" -}}
{{- if $sa.create }}true{{ end -}}
{{- else -}}
true
{{- end -}}
{{- end }}

{{- define "shepherd.serviceAccountName" -}}
{{- if include "shepherd.serviceAccountCreate" . -}}
{{- (.Values.serviceAccount).name | default (include "shepherd.fullname" .) -}}
{{- else -}}
{{- (.Values.serviceAccount).name | default "default" -}}
{{- end -}}
{{- end }}

{{/*
The Secret the RUNTIME workload reads its environment from.

existingSecret wins; otherwise the chart's own <fullname>-secrets, which is
also the name the ExternalSecret targets so the generated-secrets path needs no
special case here.
*/}}
{{- define "shepherd.secretName" -}}
{{- .Values.existingSecret | default (printf "%s-secrets" (include "shepherd.fullname" .)) -}}
{{- end }}

{{- define "shepherd.image" -}}
{{- $reg := .Values.image.registry }}
{{- $repo := .Values.image.repository }}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- if $reg }}
{{- printf "%s/%s:%s" $reg $repo $tag }}
{{- else }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}
{{- end }}

{{/*
S3 sandbox simulator (VB-1 §6.4). A distinct name, not shepherd.fullname
suffixed ad hoc, so its Service/Deployment/NetworkPolicy/ServiceAccount names
are stable and greppable on their own.
*/}}
{{- define "shepherd.simulatorFullname" -}}
{{- printf "%s-simulator" (include "shepherd.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shepherd.simulatorLabels" -}}
{{ include "shepherd.labels" . }}
app.kubernetes.io/component: simulator
{{- end }}

{{- define "shepherd.simulatorSelectorLabels" -}}
{{ include "shepherd.selectorLabels" . }}
app.kubernetes.io/component: simulator
{{- end }}

{{/*
The Secret + key holding the simulator's bearer token (W4-S3, D9), read by
BOTH consumers: the simulator's own SIM_TOKEN env var and Shepherd's
SHEPHERD_SIMULATOR_TOKEN env var. One helper for both so they can never point
at two different Secrets and silently desync -- the failure mode is every run
getting a 401 nobody can explain from the values alone.

Precedence, highest first:

  1. simulator.token.existingSecret -- the operator's own Secret.
  2. externalSecrets.enabled -- the ExternalSecret this chart renders in
     externalsecret.yaml, named the same as case 3 so the name is stable
     regardless of which of these two actually produced it.
  3. otherwise, the chart's own generated Secret
     (templates/secret-simulator-token.yaml).
*/}}
{{- define "shepherd.simulatorTokenSecretName" -}}
{{- if ((.Values.simulator.token).existingSecret) -}}
{{- .Values.simulator.token.existingSecret -}}
{{- else -}}
{{- printf "%s-token" (include "shepherd.simulatorFullname" .) -}}
{{- end -}}
{{- end }}

{{- define "shepherd.simulatorTokenKey" -}}
{{- (.Values.simulator.token).key | default "token" -}}
{{- end }}

{{/*
"true" (a non-empty string) when simulator.enabled AND the operator has not
overridden config.simulator by hand -- the one case where Shepherd's own
config is wired to THIS chart's simulator automatically, token included.
Shared between shepherd.podEnv (which env var to add) and shepherd.configYaml
(which host/port dict to auto-fill) so the two conditions cannot drift apart.
*/}}
{{- define "shepherd.simulatorAutoWired" -}}
{{- if and ((.Values.simulator).enabled) (not (hasKey (.Values.config | default dict) "simulator")) }}true{{ end -}}
{{- end }}

{{/*
"true" when the chart itself owns generating the token's Secret (D9's
precedence case 3) -- simulator.enabled, no existingSecret, no
externalSecrets.enabled. Both Deployments' checksum/simulator-token
annotations use this: existingSecret and ExternalSecret values are not known
at render time (nothing here computed them), so there is nothing to hash for
those two paths -- same reasoning as deployment.yaml's existing
checksum/secret, which is also chart-generated-content only.
*/}}
{{- define "shepherd.simulatorTokenChartGenerated" -}}
{{- if and ((.Values.simulator).enabled) (not ((.Values.simulator.token).existingSecret)) (not ((.Values.externalSecrets).enabled)) }}true{{ end -}}
{{- end }}

{{- define "shepherd.simulatorImage" -}}
{{- $reg := .Values.simulator.image.registry }}
{{- $repo := .Values.simulator.image.repository }}
{{- $tag := .Values.simulator.image.tag | default .Chart.AppVersion }}
{{- if $reg }}
{{- printf "%s/%s:%s" $reg $repo $tag }}
{{- else }}
{{- printf "%s:%s" $repo $tag }}
{{- end }}
{{- end }}

{{/*
The migrate Job's own resources: <fullname>-migrate.

The Job is a pre-install/pre-upgrade hook at weight -5, and it needs three
things to exist before it runs: the ServiceAccount it runs as, the ConfigMap it
mounts at /etc/shepherd, and the Secret it takes its env from. Helm applies ALL
hooks before ANY normal resource, so an ordinary ConfigMap does not exist yet
and the install fails in a different way for each one:

  ServiceAccount  pods "shepherd-migrate-" is forbidden: error looking up
                  service account ...: serviceaccount "shepherd" not found
  ConfigMap       MountVolume.SetUp failed for volume "config":
                  configmap "shepherd" not found
  Secret          silently absent (envFrom is optional: true), so the Job runs
                  with no SHEPHERD_DATABASE_URL and fails to connect

Each surfaces minutes later as "Job in progress" after the helm --wait timeout.
`helm template` cannot catch any of them: hook ordering only exists at install
time. Found by installing the chart into a real cluster (e2e/k8s).

The fix used to be to hook-ize the RUNTIME ConfigMap, Secret and ServiceAccount
-- the same objects the Deployment mounts. That solved the ordering and created
three worse problems, because Helm does not track hook resources in the release:

  - `helm rollback` runs pre-rollback hooks, never pre-upgrade ones. The
    ConfigMap was therefore never reverted: the Deployment rolled back to the
    old image while its pods mounted the NEW config, after the exact operation
    people run when an upgrade has gone wrong.
  - `helm uninstall` left all three behind -- including, when `secrets` was set,
    a Secret holding the database URL and the encryption key.
  - Flipping migrations.job.enabled to false turned them into tracked resources
    Helm could not adopt, failing the upgrade with "invalid ownership metadata".

So the Job gets its own copies instead, under its own name, and the runtime
objects go back to being ordinary tracked resources. These copies are pure
scratch: hook-succeeded cleans them up when the migration works, and
before-hook-creation clears a failed one out of the way of the next attempt.
*/}}
{{- define "shepherd.migrateFullname" -}}
{{- printf "%s-migrate" (include "shepherd.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "shepherd.migrateHookDeps" -}}
"helm.sh/hook": pre-install,pre-upgrade
"helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
"helm.sh/hook-weight": "-10"
{{- end }}

{{/*
The Secret the migrate Job reads, which is not always the runtime one.

  existingSecret       the user's own object; it already exists in the cluster,
                       so a hook can read it directly.
  externalSecrets      <fullname>-secrets, created by ESO from the -20
                       ExternalSecret hook, i.e. before this Job at -5.
  secrets              the chart's own values, which now render into a TRACKED
                       Secret created after all hooks -- so the Job needs the
                       -migrate hook copy.
  none of the above    <fullname>-secrets, which simply does not exist;
                       envFrom is optional: true, and the Job gets its database
                       URL from cnpg's operator-generated secret instead.
*/}}
{{- define "shepherd.migrateSecretName" -}}
{{- if .Values.existingSecret -}}
{{- .Values.existingSecret -}}
{{- else if and .Values.secrets (not ((.Values.externalSecrets).enabled)) -}}
{{- printf "%s-secrets" (include "shepherd.migrateFullname" .) -}}
{{- else -}}
{{- printf "%s-secrets" (include "shepherd.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
The port Shepherd serves /metrics on, derived from config.server.metrics_listen.

Derived rather than configured separately so the container port, the metrics
Service, and the address the server actually binds cannot drift apart — which
is how the ServiceMonitor came to scrape a port that had never served metrics.
Accepts ":9090" and "0.0.0.0:9090" alike by taking the last colon-separated
field.
*/}}
{{- define "shepherd.metricsPort" -}}
{{- $listen := (((.Values.config).server).metrics_listen) | default ":9090" -}}
{{- $parts := splitList ":" $listen -}}
{{- index $parts (sub (len $parts) 1) -}}
{{- end }}

{{/*
The CNPG Cluster's name. Defaults to "<fullname>-db" rather than reusing the
release name, so the Cluster and the Deployment cannot collide.
*/}}
{{- define "shepherd.cnpgClusterName" -}}
{{- .Values.cnpg.name | default (printf "%s-db" (include "shepherd.fullname" .)) -}}
{{- end }}

{{/*
Hook annotations for the resources the migration Job depends on EXISTING before
it runs: the CNPG Cluster and the ExternalSecret. Weight -20 puts them ahead of
the ConfigMap/Secret at -10 and the Job itself at -5.

These resources own state, so they must never be recreated: deleting a CNPG
Cluster takes the database with it, and recreating an ExternalSecret makes it
sync once more against fresh Password generators, replacing an encryption key
that cannot be rotated.

Omitting hook-delete-policy does NOT achieve that. Helm's default is
before-hook-creation, so an omitted policy deletes the previous resource on
every upgrade -- the exact opposite of what an earlier version of this comment
claimed. Nor can a policy express "never": the three values are
before-hook-creation, hook-succeeded and hook-failed, and specifying only the
latter two leaves the create to collide with the existing object.

The protection is therefore not an annotation at all. The templates that use
this helper render ONLY when the resource does not already exist (see the
lookup guard in cnpg-cluster.yaml and externalsecret.yaml); a hook absent from
the manifest is never processed, so the live one is left alone.
*/}}
{{- define "shepherd.bootstrapHookDeps" -}}
"helm.sh/hook": pre-install,pre-upgrade
"helm.sh/hook-weight": "-20"
{{- end }}

{{/*
The container env every Shepherd process needs, shared by the Deployment and
the migration Job so the two cannot disagree about where the database is.

When CNPG provisions the database, SHEPHERD_DATABASE_URL comes from the `uri`
key of the Secret the operator generates. An explicit `env` entry beats
`envFrom`, so this deliberately overrides a SHEPHERD_DATABASE_URL that happens
to be in the user's own secret -- with cnpg.enabled the chart owns the
database, and silently connecting somewhere else would be worse than loud.
*/}}
{{- define "shepherd.podEnv" -}}
{{- /*
  Built as a list of chunks joined with newlines rather than by emitting YAML
  inline. Whitespace control across two optional blocks is the kind of thing
  that renders perfectly until both are set at once and then silently welds the
  last line of one onto the first line of the other.
*/ -}}
{{- $chunks := list -}}
{{- if ((.Values.cnpg).enabled) -}}
{{- $chunks = append $chunks (printf "- name: SHEPHERD_DATABASE_URL\n  valueFrom:\n    secretKeyRef:\n      name: %s-app\n      key: uri" (include "shepherd.cnpgClusterName" .)) -}}
{{- end -}}
{{- with .Values.extraEnv -}}
{{- $chunks = append $chunks (trimSuffix "\n" (toYaml .)) -}}
{{- end -}}
{{- join "\n" $chunks -}}
{{- end }}

{{/*
Shepherd's SHEPHERD_SIMULATOR_TOKEN env entry, for the app Deployment ONLY.

Deliberately NOT part of shepherd.podEnv: the pre-install migration Job shares
that helper, and the token Secret is an ordinary chart resource that does not
exist yet while pre-install hooks run. With the entry in podEnv every install
died in CreateContainerConfigError ("secret <release>-simulator-token not
found") until helm's wait expired (kind gate, 2026-09-11). The migration needs
the database, never the simulator.
*/}}
{{- define "shepherd.simulatorTokenEnv" -}}
{{- if eq (include "shepherd.simulatorAutoWired" .) "true" -}}
{{- /*
  Only when this chart auto-wires Shepherd to its own simulator -- an
  explicit config.simulator block supplies its own token verbatim (and
  shepherd.configYaml refuses to render at all if it doesn't), so adding this
  env var there too would just be a second, unused source of truth.
*/ -}}
{{- printf "- name: SHEPHERD_SIMULATOR_TOKEN\n  valueFrom:\n    secretKeyRef:\n      name: %s\n      key: %s" (include "shepherd.simulatorTokenSecretName" .) (include "shepherd.simulatorTokenKey" .) -}}
{{- end -}}
{{- end }}

{{/*
The pod-level securityContext every Shepherd pod runs with.

Shared so the migration Job cannot drift from the Deployment. It did: the Job
carried only the container-level trio (read-only root, no privilege escalation,
all capabilities dropped) and none of the pod-level fields. On a namespace
enforcing the `restricted` Pod Security Standard that is a rejected pod -- and
because the Job is a pre-install hook, a rejected pod fails the whole install,
while the Deployment it was migrating for would have been admitted fine.
*/}}
{{- define "shepherd.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/*
The container-level securityContext, shared for the same reason.
*/}}
{{- define "shepherd.containerSecurityContext" -}}
readOnlyRootFilesystem: true
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
{{- end }}

{{/*
The shepherd.yaml body, shared by the runtime ConfigMap and the migration Job's
hook copy so the two cannot disagree about the database or the simulator.

With the simulator enabled and no operator-supplied config.simulator block, wire
shepherd to this chart's own simulator Service. The viper defaults happen to
match only when the release is literally named "shepherd"; templating the
Service DNS makes any release name work. control_url keeps the Service name
(dialled from Shepherd's own Pod, a different network namespace); the other
four fields are 127.0.0.1 (D10 -- they are dialled by the sandboxed Alloy
CHILD PROCESS sharing the simulator Pod's own netns, which has no cluster DNS
egress to resolve a Service name with any more). An explicit
.Values.config.simulator always wins verbatim, and -- once this chart started
enforcing a token -- must supply its own `token` or the render refuses to ship
an unauthenticated control API silently.
*/}}
{{- define "shepherd.configYaml" -}}
{{- $cfg := deepCopy .Values.config }}
{{- if hasKey $cfg "simulator" }}
{{- /*
  An explicit config.simulator block always wins verbatim -- it is how an
  operator points Shepherd at a simulator this chart did not deploy. It
  bypasses simulator.token entirely (that only feeds the AUTO-wired path
  below), so nothing else here can supply the control API's token for it.
  Skipping the check would ship an unauthenticated control API silently
  wherever this block is used with nothing after "token" -- the config.simulator
  the operator wrote before this chart enforced auth at all still renders
  and still runs, just without ever meaning to leave the API open.
*/ -}}
{{- if and (not (hasKey $cfg.simulator "token")) (ne (($cfg.simulator).enabled) false) }}
{{- fail "config.simulator is set explicitly but has no `token`: the control API this chart wires Shepherd to must be authenticated. Add config.simulator.token (the shared secret your simulator expects), or remove config.simulator and let simulator.enabled/simulator.token wire it up automatically." }}
{{- end }}
{{- else if ((.Values.simulator).enabled) }}
{{- $sim := include "shepherd.simulatorFullname" . }}
{{- $_ := set $cfg "simulator" (dict
      "enabled" true
      "control_url" (printf "http://%s:8099" $sim)
      "capture_base_url" "http://127.0.0.1:9110"
      "otlp_grpc_address" "127.0.0.1:4317"
      "syslog_host" "127.0.0.1"
      "target_address" "127.0.0.1:9111") }}
{{- end }}
{{- toYaml $cfg }}
{{- end }}

{{/*
The smallest number of Shepherd pods that will ever be running.

The PodDisruptionBudget has to key off this rather than .Values.replicas, which
is not the replica count at all once an HPA owns it: with autoscaling on,
minReplicas: 1 and the default replicas: 2, the old gate rendered a
minAvailable: 1 budget that deadlocks every node drain the moment the
autoscaler scales to one pod -- and the reverse, replicas: 1 with autoscaling
up to 10, left a multi-pod deployment with no budget at all.
*/}}
{{- define "shepherd.effectiveMinReplicas" -}}
{{- if ((.Values.autoscaling).enabled) -}}
{{- .Values.autoscaling.minReplicas | default 2 -}}
{{- else -}}
{{- .Values.replicas -}}
{{- end -}}
{{- end }}

{{/*
Receiver tier (#109, docs/archive/plans/2026-09-28-receiver-tier.md): an OTLP/HTTP
Alloy that the gateway fronts and that forwards each tenant's data with the
tenant header the gateway injected. Its own stable name, like the simulator's.
*/}}
{{- define "shepherd.receiverFullname" -}}
{{- printf "%s-receiver" (include "shepherd.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "shepherd.receiverLabels" -}}
{{ include "shepherd.labels" . }}
app.kubernetes.io/component: receiver
{{- end }}

{{- define "shepherd.receiverSelectorLabels" -}}
{{ include "shepherd.selectorLabels" . }}
app.kubernetes.io/component: receiver
{{- end }}

{{/*
The receiver config file `shepherd receiver render` reads (internal/cli/
receiver.go's format). One exporter per enabled signal; a secret-sourced
endpoint or credential becomes an env var name the Alloy container gets from
its Secret, so no secret value is ever written into this ConfigMap.
*/}}
{{- define "shepherd.receiverConfig" -}}
{{- $r := .Values.receiver -}}
otlp:
  - label: tenants
    mode: {{ $r.mode | quote }}
    http:
      listen_addr: {{ printf "0.0.0.0:%d" (int $r.listener.port) | quote }}
      max_request_body_size: {{ required "receiver.listener.maxRequestBodySize is required" $r.listener.maxRequestBodySize | quote }}
    batch:
      timeout: {{ $r.batch.timeout | quote }}
      send_batch_size: {{ int $r.batch.sendBatchSize }}
      send_batch_max_size: {{ int $r.batch.sendBatchMaxSize }}
{{- range $signal := list "metrics" "logs" "traces" }}
{{- $e := index $r.exporters $signal }}
{{- if $e.enabled }}
    {{ $signal }}:
      name: {{ $signal }}
      protocol: {{ $e.protocol | quote }}
      {{- if $e.endpointFromSecret.name }}
      endpoint_env: {{ printf "RECEIVER_%s_ENDPOINT" (upper $signal) | quote }}
      {{- else }}
      endpoint: {{ required (printf "receiver.exporters.%s: set endpoint or endpointFromSecret" $signal) $e.endpoint | quote }}
      {{- end }}
      {{- if $e.authorizationFromSecret.name }}
      secret_header_env:
        Authorization: {{ printf "RECEIVER_%s_AUTHORIZATION" (upper $signal) | quote }}
      {{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Tenant-route apply (docs/archive/plans/2026-09-29-tenant-route-apply.md): "true" when
Shepherd should apply tenant routes' HTTPRoutes itself -- only with the receiver
tier on, since the routes point at the receiver Service. Gates the RBAC, the
pod's token mount and the reconciler's env; empty otherwise.
*/}}
{{- define "shepherd.routeApplyEnabled" -}}
{{- if and ((.Values.receiver).enabled) (ne ((.Values.receiver).applyTenantRoutes) false) -}}
true
{{- end -}}
{{- end }}

{{/*
The reconciler's env, for the app Deployment only: its namespace from the
downward API, and the receiver Service this chart renders as the backend.
*/}}
{{- define "shepherd.routeApplyEnv" -}}
{{- if include "shepherd.routeApplyEnabled" . -}}
- name: SHEPHERD_GATEWAY_ROUTES_APPLY_ENABLED
  value: "true"
- name: SHEPHERD_GATEWAY_ROUTES_APPLY_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_SERVICE
  value: {{ include "shepherd.receiverFullname" . | quote }}
- name: SHEPHERD_GATEWAY_ROUTES_APPLY_BACKEND_PORT
  value: {{ .Values.receiver.listener.port | quote }}
{{- end -}}
{{- end }}

{{/*
The gateway's public URL, for the "connect an app" snippets on the Tenant
routes page. App Deployment only; with the receiver on and the value set.
*/}}
{{- define "shepherd.publicBaseURLEnv" -}}
{{- if and ((.Values.receiver).enabled) ((.Values.receiver).publicBaseURL) -}}
{{- if not (hasPrefix "https://" .Values.receiver.publicBaseURL) -}}
{{- fail "receiver.publicBaseURL must be an https URL: apps send telemetry to it from outside the cluster" -}}
{{- end -}}
- name: SHEPHERD_GATEWAY_ROUTES_PUBLIC_BASE_URL
  value: {{ .Values.receiver.publicBaseURL | quote }}
{{- end -}}
{{- end }}
