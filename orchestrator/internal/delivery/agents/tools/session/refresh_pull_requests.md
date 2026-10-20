Read a linked project's task's open pull requests from GitHub now, and list them as pull_requests with that task does. pull_requests answers dude's last read of GitHub (lastPolledAt); use this when that is too old. Each open pull request it reads is one of the session's 60 GitHub reads an hour; with too few left for all of them, it reads none.

{{- define "tools/session/refresh_pull_requests/project" -}}
{{template "tools/session/findings/project"}}
{{- end}}

{{- define "tools/session/refresh_pull_requests/task" -}}
{{template "tools/session/findings/task"}}
{{- end}}
