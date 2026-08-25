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

{{- define "shepherd.selectorLabels" -}}
app.kubernetes.io/name: {{ include "shepherd.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
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
shepherd.migrateHookDeps renders the hook annotations that a resource needs when
the migrate Job is enabled.

The migrate Job is a pre-install/pre-upgrade hook at weight -5, and it depends on
three ordinary resources: the ServiceAccount it runs as, the ConfigMap it mounts
at /etc/shepherd, and the Secret it takes its env from. Helm applies ALL hooks
before ANY normal resource, so without this those three do not exist yet and the
install fails in a different way for each one:

  ServiceAccount  pods "shepherd-migrate-" is forbidden: error looking up
                  service account ...: serviceaccount "shepherd" not found
  ConfigMap       MountVolume.SetUp failed for volume "config":
                  configmap "shepherd" not found
  Secret          silently absent (envFrom is optional: true), so the Job runs
                  with no SHEPHERD_DATABASE_URL and fails to connect

Each surfaces minutes later as "Job in progress" after the helm --wait timeout.
`helm template` cannot catch any of them: hook ordering only exists at install
time. Found by installing the chart into a real cluster (e2e/k8s).

Weight -10 orders these ahead of the Job at -5.

On hook-delete-policy: NOT setting it does not mean "never deleted". Helm's
documented default is before-hook-creation, so each of these IS deleted and
recreated on every install and upgrade. For a ConfigMap/Secret/ServiceAccount
that is survivable — they are recreated immediately with the same content, and
it is what lets a re-install over leftovers succeed. It is NOT survivable for a
resource that owns data; see shepherd.bootstrapHookDeps.
*/}}
{{- define "shepherd.migrateHookDeps" -}}
{{- if .Values.migrations.job.enabled }}
"helm.sh/hook": pre-install,pre-upgrade
"helm.sh/hook-weight": "-10"
{{- end }}
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
{{- if .Values.cnpg.enabled -}}
{{- $chunks = append $chunks (printf "- name: SHEPHERD_DATABASE_URL\n  valueFrom:\n    secretKeyRef:\n      name: %s-app\n      key: uri" (include "shepherd.cnpgClusterName" .)) -}}
{{- end -}}
{{- with .Values.extraEnv -}}
{{- $chunks = append $chunks (trimSuffix "\n" (toYaml .)) -}}
{{- end -}}
{{- join "\n" $chunks -}}
{{- end }}
