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
