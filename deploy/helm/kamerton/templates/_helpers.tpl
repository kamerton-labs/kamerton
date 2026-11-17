{{/* Copyright 2026 The Kamerton Authors. SPDX-License-Identifier: AGPL-3.0-only */}}

{{- define "kamerton.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "kamerton.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else if contains (include "kamerton.name" .) .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "kamerton.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "kamerton.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "kamerton.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "kamerton.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kamerton.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "kamerton.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kamerton.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* Endpoint resolution: bring-your-own URL wins, else the managed program if enabled, else empty. */}}
{{- define "kamerton.prometheusURL" -}}
{{- if .Values.bringYourOwn.prometheus.enabled }}{{ .Values.bringYourOwn.prometheus.url }}
{{- else if (index .Values "kube-prometheus-stack" "enabled") }}{{ .Values.endpoints.prometheus }}
{{- end }}
{{- end }}

{{- define "kamerton.alertmanagerURL" -}}
{{- if .Values.bringYourOwn.prometheus.enabled }}{{ .Values.bringYourOwn.prometheus.alertmanagerUrl }}
{{- else if (index .Values "kube-prometheus-stack" "enabled") }}{{ .Values.endpoints.alertmanager }}
{{- end }}
{{- end }}

{{- define "kamerton.grafanaURL" -}}
{{- if .Values.bringYourOwn.grafana.enabled }}{{ .Values.bringYourOwn.grafana.url }}
{{- else if .Values.grafana.enabled }}{{ .Values.endpoints.grafana }}
{{- end }}
{{- end }}

{{/* Login: the Secret with the user name and password, empty when login is off. */}}
{{- define "kamerton.authSecretName" -}}
{{- if .Values.auth.existingSecret }}{{ .Values.auth.existingSecret }}
{{- else if and .Values.auth.username .Values.auth.password }}{{ include "kamerton.fullname" . }}-auth
{{- end }}
{{- end }}
