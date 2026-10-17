{{if not .Repos -}}
No repository is checked out for this task: it changes no code.
{{- else -}}
Your checkout is the task's head:{{range .Repos}}
- `{{.Name}}` at `{{.Path}}` ({{if .ReadOnly}}read only{{else}}you may edit it, small things only{{end}}){{end}}{{with .TaskBranch}}

The task branch is `{{.}}`; dude brings it in as `lux/{{.}}`.{{end}}
{{- end}}
