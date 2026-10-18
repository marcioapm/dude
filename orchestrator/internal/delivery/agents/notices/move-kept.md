{{if .Reset -}}
You were to move back to {{.To}}; lux kept you on {{.Kept}}: {{.Why}}.
{{- else -}}
You asked for {{.To}}; lux kept you on {{.Kept}}: {{.Why}}
{{- end}}
