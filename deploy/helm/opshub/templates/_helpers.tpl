{{/* Names and labels. */}}
{{- define "opshub.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- else -}}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "opshub.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "opshub.selector" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* image: registry/repository:tag */}}
{{- define "opshub.image" -}}
{{- $repo := .image.repository -}}
{{- if .root.Values.image.registry -}}{{- $repo = printf "%s/%s" (trimSuffix "/" .root.Values.image.registry) $repo -}}{{- end -}}
{{- printf "%s:%s" $repo (.root.Values.image.tag | default .root.Chart.AppVersion) -}}
{{- end -}}

{{/* Restricted pod security (non-root, read-only root filesystem, no privileges). */}}
{{- define "opshub.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: { drop: [ALL] }
{{- end -}}

{{- define "opshub.podSecurity" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
fsGroup: {{ .uid }}
seccompProfile: { type: RuntimeDefault }
{{- end -}}

{{/* Environment shared by the API and the migration job. */}}
{{- define "opshub.env" -}}
- name: OPSHUB_ENV
  value: {{ .Values.environment | quote }}
- name: OPSHUB_PUBLIC_URL
  value: {{ required "publicURL is required" .Values.publicURL | quote }}
- name: OPSHUB_LOG_FORMAT
  value: json
- name: OPSHUB_HTTP_ADDR
  value: ":8080"
- name: OPSHUB_BLOB_DIR
  value: /data/blobs
- name: OPSHUB_MIGRATE_ON_START
  value: {{ (not .Values.migrations.enabled) | quote }}
{{- with .Values.api.trustedProxies }}
- name: OPSHUB_TRUSTED_PROXIES
  value: {{ . | quote }}
{{- end }}
{{- range $k, $v := .Values.config }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}
