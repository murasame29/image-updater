{{/* Expand the chart name. */}}
{{- define "image-updater.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Create a default fully qualified app name. */}}
{{- define "image-updater.fullname" -}}
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

{{/* Chart name and version for labels. */}}
{{- define "image-updater.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. */}}
{{- define "image-updater.labels" -}}
helm.sh/chart: {{ include "image-updater.chart" . | quote }}
{{ include "image-updater.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{/* Selector labels. */}}
{{- define "image-updater.selectorLabels" -}}
app.kubernetes.io/name: {{ include "image-updater.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end }}

{{/* ServiceAccount name. */}}
{{- define "image-updater.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "image-updater.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* ConfigMap name. */}}
{{- define "image-updater.configMapName" -}}
{{- default (include "image-updater.fullname" .) .Values.config.existingConfigMap }}
{{- end }}

{{/* Container image reference. */}}
{{- define "image-updater.image" -}}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository (required "image.tag is required when image.digest is empty" .Values.image.tag) }}
{{- end }}
{{- end }}
