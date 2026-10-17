{{if eq .Status "resolved" -}}
fixed{{with .By}}, by {{.}}{{end}}
{{- else if eq .Status "superseded" -}}
superseded
{{- else if gt .Attempts 0 -}}
open, {{.Attempts}} fix attempts
{{- else -}}
open
{{- end}}
