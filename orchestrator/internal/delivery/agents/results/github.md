{{define "results/github/refused"}}
{{- if eq .Kind "moved"}}repository {{.Slug}} has moved on GitHub; update the project's repository URL
{{- else if eq .Kind "moved_item"}}GitHub says {{.What}} has moved (renamed repository or transferred issue)
{{- else if eq .Kind "redirect"}}GitHub answered reading {{.What}} with a redirect (HTTP {{.Status}}), which dude does not follow
{{- else if eq .Kind "too_large"}}GitHub's answer reading {{.What}} is past the 4 MiB an agent's read may take
{{- else if eq .Kind "rate_reserve"}}the rest of GitHub's rate limit is kept for delivery: read {{.What}} {{template "results/github/later" .Wait}}, or work with what you read
{{- else if eq .Kind "rate_limited"}}GitHub is rate limiting dude (HTTP {{.Status}}): read {{.What}} {{template "results/github/later" .Wait}}
{{- else if eq .Kind "not_found"}}GitHub has no {{.What}}, or dude cannot see it
{{- else if and (eq .Kind "unavailable") (not .Status)}}GitHub did not answer reading {{.What}}: try again later
{{- else if eq .Kind "unavailable"}}GitHub did not answer reading {{.What}} (HTTP {{.Status}}): try again later
{{- else if .Lacks}}dude's GitHub connection lacks {{range $i, $set := .Lacks}}{{if $i}}, or {{end}}{{range $j, $p := $set}}{{if $j}} and {{end}}{{$p}}{{end}}{{end}}, so GitHub refused reading {{.What}} (HTTP {{.Status}})
{{- else}}GitHub refused reading {{.What}} (HTTP {{.Status}})
{{- end}}
{{- if and .Issue (eq .Kind "forbidden")}}; a pull request's conversation is read with github_pull_request{{end}}{{end}}

{{- define "results/github/later"}}{{with .}}in {{.}}{{else}}later{{end}}{{end}}

{{- define "results/github/whose-task"}}your task's project{{end}}

{{- define "results/github/whose-session"}}the projects linked to this session{{end}}

{{- define "results/github/cut"}}…cut{{end}}

{{- define "results/github/search-more"}}{{.}} more match: narrow the query{{end}}

{{- define "results/github/issue-pull-request"}}a pull request: github_pull_request reads its conversation comments, reviews, threads, checks and files{{end}}

{{- define "results/github/comments-cut"}}{{.Cut}}: the first {{.Shown}} of {{.Total}} comments{{end}}

{{- define "results/github/comments-newest-cut"}}{{.Cut}}: the newest {{.Shown}} of {{.Total}} comments{{end}}

{{- define "results/github/no-diff"}}GitHub shows no diff for it (binary, or too large){{end}}

{{- define "results/github/checks-cut"}}{{.}}: check runs incomplete{{end}}

{{- define "results/github/reviews-cut"}}{{.Cut}}: at most {{.Max}} reviews; review history incomplete{{end}}

{{- define "results/github/threads-unknown"}}{{.}}: GitHub would not list all review threads{{end}}

{{- define "results/github/files-cut"}}{{.Cut}}: the first {{.Max}} changed files{{end}}

{{- define "results/github/budget-cut"}}{{.}}: text and list tails omitted to fit the result budget{{end}}

{{- define "results/github/text-cut"}} {{.Cut}}: {{.Kept}} of {{.Total}} bytes{{end}}

{{- define "results/github/run-jobs-next"}}read a failed job's log with github_check_log, its id as checkId{{end}}

{{- define "results/github/run-jobs-cut"}}{{.Cut}}: the first {{.Shown}} of {{.Total}} jobs{{end}}
