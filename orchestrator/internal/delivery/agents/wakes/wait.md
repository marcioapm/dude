{{- if .Now}}It holds already ({{.Held}} of {{.Of}}):
{{- else if not .Of}}Your wait {{.ID}} reached its deadline: it was a timer.
{{- else if .Deadline}}Your wait {{.ID}} ended at its deadline ({{.Held}} of {{.Of}} held{{if .Any}}, any one was enough{{end}}){{if .Items}}:{{else}}.{{end}}
{{- else}}Your wait {{.ID}} held ({{.Held}} of {{.Of}}{{if .Any}}, any one was enough{{end}}):
{{- end}}
{{- range $i, $it := .Items}}{{if $i}};{{end}} {{template "wakes/wait/item" $it}}{{end}}{{if .Items}}.{{end}}
{{- with .Meanwhile}} Meanwhile: {{range $i, $c := .}}{{if $i}}, {{end}}{{template "wakes/wait/meanwhile" $c}}{{end}}.{{end}}
{{- with .Note}} Your note: {{printf "%q" .}}{{end}}

{{- define "wakes/wait/item"}}
{{- if eq .Kind "run"}}{{.Who}} {{.RunID}}{{with .Was}} (restarted from {{.}}){{end}}
{{- if eq .Status "completed"}} completed
{{- else if .Ended}} {{.Status}}{{with .Error}}: {{clip (oneLine .) 120}}{{end}}
{{- else}} not ended ({{.Status}})
{{- end}}
{{- else if eq .State "merged"}}{{.PR}} was merged
{{- else if eq .State "closed"}}{{.PR}} was closed
{{- else if .MovedTo}}{{.PR}}'s head moved to {{short .MovedTo}}: its checks will never finish on {{short .Head}}
{{- else if eq .Checks "failing"}}{{.PR}} checks failing at {{short .Head}}{{with .Failing}} ({{join . ", "}}){{end}}
{{- else if eq .Checks "passing"}}{{.PR}} checks passing at {{short .Head}}
{{- else}}{{.PR}} checks not final at {{short .Head}} ({{.Checks}}{{with .Polled}}, last read {{.}}{{end}})
{{- end}}
{{- end}}

{{- define "wakes/wait/meanwhile"}}
{{- if eq .What "commented"}}{{.N}} pull request comment{{if gt .N 1}}s{{end}}
{{- else if eq .What "reviewed"}}{{.N}} pull request review{{if gt .N 1}}s{{end}}
{{- else if eq .What "feedback"}}{{.N}} feedback submission{{if gt .N 1}}s{{end}}
{{- else}}{{.N}} message{{if gt .N 1}}s{{end}} from people in Chat
{{- end}}
{{- end}}
