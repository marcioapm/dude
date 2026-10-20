{{define "results/limit-request/limit"}}
{{- if eq . "perReviewRound"}}the pull request fix rounds per review round (delivery policy)
{{- else if eq . "perPullRequest"}}the fix rounds per pull request (organisation GitHub settings)
{{- else}}the failed-check reruns on one pull request head
{{- end}}{{end}}

{{- define "results/limit-request/question"}}The conductor asks to raise {{template "results/limit-request/limit" .Limit}} for this task's delivery, from {{.From}} to {{.To}}
{{- if eq .Limit "rerun"}} on {{.Repo}}#{{.Number}} at {{short .Head}}{{end}}. Its reason: {{.Reason}}
{{- if ne .Limit "rerun"}}

Only an organisation admin may approve it, as only an admin may change that setting. It applies to this task alone; the setting stays as it is.{{end}}{{end}}

{{- define "results/limit-request/choice"}}
{{- if eq .Limit "perReviewRound"}}Allow {{.Extra}} more fix {{if eq .Extra 1}}round{{else}}rounds{{end}} per review round for this task
{{- else if eq .Limit "perPullRequest"}}Allow {{.Extra}} more fix {{if eq .Extra 1}}round{{else}}rounds{{end}} per pull request for this task
{{- else}}Allow one more rerun on {{.Repo}}#{{.Number}} at {{short .Head}}
{{- end}}{{end}}

{{- define "results/limit-request/no"}}No{{end}}

{{- define "results/limit-request/not-allowed"}}only an organisation admin may raise {{template "results/limit-request/limit" .Limit}}: it is a setting only an admin may change{{end}}

{{- define "results/limit-request/asked"}}Asked the task's people to raise it; end your turn. Their answer is your next message; once approved, limits shows the new bound and the check allows it.{{end}}
