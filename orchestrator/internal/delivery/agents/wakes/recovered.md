{{if eq .Status "done" -}}
Your recovery {{.ID}} of {{.Run}} pushed its commits to {{.Branch}}: {{join .Pushed ", "}}{{if .Other}}; {{join .Other "; "}}{{end}}.{{if .Dirty}} Uncommitted work in {{join .Dirty ", "}} was lost.{{end}} Have a writer merge it, then review it.
{{- else if eq .Status "refused" -}}
Your recovery {{.ID}} of {{.Run}} was refused, nothing pushed: {{clip (oneLine .Error) .WhyChars}}
{{- if .Expired}}. Tell the person: its commits may hold a secret value dude can no longer check for; anything taken from lux's checkpoint must be checked for secrets by hand before it is pushed anywhere.{{end}}
{{- else -}}
Could not recover {{.Run}} ({{.ID}}), nothing pushed: {{clip (oneLine .Error) .WhyChars}}{{if .Other}}; {{join .Other "; "}}{{end}}
{{- end}}
