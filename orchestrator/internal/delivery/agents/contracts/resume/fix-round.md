## A new round

You worked on this branch before, and there is more to fix: your checkout{{if gt (len .Ranges) 1}}s are{{else}} is{{end}} now at the head this round starts from.{{if .Ranges}} What changed since your last push: {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}}.{{end}}{{with .Contract}}

## How you fix

{{.}}{{end}}{{range .Sections}}

{{.}}{{end}}