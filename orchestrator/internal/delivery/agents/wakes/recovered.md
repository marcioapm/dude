{{if eq .Status "done" -}}
Your recovery {{.ID}} of {{.Run}} pushed its commits to {{.Branch}}: {{join .Pushed ", "}}{{if .Other}}; {{join .Other "; "}}{{end}}.{{if .Dirty}} Uncommitted work in {{join .Dirty ", "}} was lost.{{end}} Have a writer merge it, then review it.
{{- else if eq .Status "refused" -}}
Your recovery {{.ID}} of {{.Run}} was refused, nothing pushed: {{clip (oneLine .Error) 200}}
{{- else -}}
Could not recover {{.Run}} ({{.ID}}), nothing pushed: {{clip (oneLine .Error) 200}}{{if .Other}}; {{join .Other "; "}}{{end}}
{{- end}}
