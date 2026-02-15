{{- define "nereid.name" -}}
nereid
{{- end -}}

{{- define "nereid.labels" -}}
app.kubernetes.io/name: {{ include "nereid.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
