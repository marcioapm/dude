## A new round

{{if .Test -}}
You tested this branch before. It has changed since: your checkout is now at the commit{{if gt (len .Ranges) 1}}s{{end}} below, and {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}} is what changed since your last report. Test again from the start: run every criterion's check, not only those the change touched, and report every criterion in one report as before, with fresh evidence.
{{- else -}}
You reviewed this branch before. It has changed since: your checkout is now at the commit{{if gt (len .Ranges) 1}}s{{end}} below, and {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}} is what changed since your last review.{{with .Reason}} {{.}}{{end}}
{{- end}}

{{template "contracts/resume/anti-anchoring"}}{{with .Findings}}

{{.}}{{end}}{{if not .Test}}

{{.Format}}

{{.Summary}}{{end}}
