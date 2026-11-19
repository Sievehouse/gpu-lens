{{- define "gpulens.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gpulens.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "gpulens.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "gpulens.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "gpulens.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gpulens.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gpulens.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "gpulens.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "gpulens.authSecret" -}}
{{- if .Values.auth.existingSecret -}}{{ .Values.auth.existingSecret }}{{- else -}}{{ include "gpulens.fullname" . }}-auth{{- end -}}
{{- end -}}

{{- define "gpulens.alertSecret" -}}
{{- if .Values.alerts.existingSecret -}}{{ .Values.alerts.existingSecret }}{{- else -}}{{ include "gpulens.fullname" . }}-alerts{{- end -}}
{{- end -}}

{{- define "gpulens.alertsOn" -}}
{{- if or .Values.alerts.existingSecret .Values.alerts.slackWebhookUrl .Values.alerts.webhookUrl -}}true{{- end -}}
{{- end -}}
