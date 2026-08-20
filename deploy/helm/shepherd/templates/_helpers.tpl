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

Weight -10 orders these ahead of the Job at -5. hook-delete-policy is
deliberately NOT set: these resources must SURVIVE the hook phase, because the
Deployment references them too — a before-hook-creation policy would delete them
out from under a running release on upgrade.
*/}}
{{- define "shepherd.migrateHookDeps" -}}
{{- if .Values.migrations.job.enabled }}
"helm.sh/hook": pre-install,pre-upgrade
"helm.sh/hook-weight": "-10"
{{- end }}
{{- end }}
