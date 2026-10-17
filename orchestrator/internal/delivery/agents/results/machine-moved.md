Moved: you run on {{.Name}} ({{.Spec}}){{with .Pool}} in the {{.}} pool{{end}} from your next step. Your conversation and your checkout are kept, committed or not, with everything else on /workspace and your home. Running processes and /tmp are not: start your servers and watchers again. {{if .Session -}}
This session's agent runs on {{.Name}} from now on.
{{- else if eq .Role "implementer" -}}
Later implementer and fixer Runs in this task run on {{.Name}} too.
{{- else if eq .Role "conductor" -}}
Later conductors in this task run on {{.Name}} too.
{{- else -}}
Later {{.Label}} Runs in this task run on {{.Name}} too.
{{- end}} End your turn now; you are resumed on {{.Name}}.
