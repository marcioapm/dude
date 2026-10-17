{{if not . -}}
No repository is checked out for this session.
{{- else -}}
Your checkouts, read only (never pushed):{{range .}}
- `{{.Name}}` at `{{.Path}}`{{end}}
{{- end}}
