## A new round

{{if .Test -}}
You tested this branch before, and it has changed since: your checkout{{if gt (len .Ranges) 1}}s are{{else}} is{{end}} now at the head this round tests.{{if .Ranges}} What changed since your last report: {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}}.{{end}} Test again from the start: run every criterion's check, not only those the change touched, and report every criterion in one report as before, with fresh evidence.
{{- else -}}
You reviewed this branch before, and it has changed since: your checkout{{if gt (len .Ranges) 1}}s are{{else}} is{{end}} now at the head this round reviews.{{if .Ranges}} What changed since your last review: {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}}.{{end}}{{with .Reason}} {{.}}{{end}}
{{- end}}

{{template "contracts/resume/anti-anchoring"}}{{with .Findings}}

{{.}}{{end}}{{with .Questions}}

{{.}}{{end}}{{with .LastFix}}

{{.}}{{end}}{{if not .Test}}

{{.Format}}

{{.Summary}}{{end}}
