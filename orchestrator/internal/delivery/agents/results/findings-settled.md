{{if eq .Status "resolved" -}}
fixed{{with .By}} by {{.}}{{end}}
{{- else if eq .Status "accepted" -}}
{{.Accepted}}
{{- else if eq .Status "superseded" -}}
superseded: the code it described is gone
{{- else if gt .Attempts 0 -}}
open after {{.Attempts}} fix attempts
{{- else -}}
open
{{- end}}
