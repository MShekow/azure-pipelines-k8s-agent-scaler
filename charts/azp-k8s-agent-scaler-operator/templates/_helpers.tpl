{{/*
Expand the name of the chart.
*/}}
{{- define "azp-k8s-agent-scaler-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "azp-k8s-agent-scaler-operator.fullname" -}}
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

{{- define "azp-k8s-agent-scaler-operator.image" -}}
{{- $versionTag := default .Chart.AppVersion .Values.image.tag }}
{{- if .Values.image.registry }}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $versionTag }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository $versionTag }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "azp-k8s-agent-scaler-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "azp-k8s-agent-scaler-operator.labels" -}}
helm.sh/chart: {{ include "azp-k8s-agent-scaler-operator.chart" . }}
{{ include "azp-k8s-agent-scaler-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "azp-k8s-agent-scaler-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "azp-k8s-agent-scaler-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
