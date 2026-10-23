## A new round

{{if .Test -}}
You tested this branch before, and it has changed since: your checkout{{if gt (len .Ranges) 1}}s are{{else}} is{{end}} now at the head this round tests.{{if .Ranges}} What changed since your last report: {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}}.{{end}}
{{- if .CarryFrom}} Read that change and decide which criteria it can affect, by what the changed code does and what each criterion exercises, not by file names alone. Test again, with fresh evidence, every criterion it can affect, every criterion a finding below concerns, and every criterion that did not pass in your last report. Report every other criterion as `status: carried` with `from_round: {{.CarryFrom}}`, and an `observed` line saying why the change cannot affect it; dude keeps your last verdict on it only if that verdict was a pass. When in doubt, test it again. Report every criterion in one report as before.
{{- else}} This round tests afresh: run every criterion's check and report every criterion in one report as before, with fresh evidence. No criterion may be carried from an earlier round.
{{- end}}
{{- else -}}
You reviewed this branch before, and it has changed since: your checkout{{if gt (len .Ranges) 1}}s are{{else}} is{{end}} now at the head this round reviews.{{if .Ranges}} What changed since your last review: {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`git log {{$r.From}}..HEAD`{{end}}.{{end}}{{with .Reason}} {{.}}{{end}}
{{- end}}

{{template "contracts/resume/anti-anchoring"}}{{with .Findings}}

{{.}}{{end}}{{with .Questions}}

{{.}}{{end}}{{with .LastFix}}

{{.}}{{end}}{{if not .Test}}

{{.Format}}

{{.Summary}}{{end}}
