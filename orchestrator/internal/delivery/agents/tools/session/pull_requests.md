A linked project's pull requests: with task, that task's in full (checks, reviews, feedback; name feedback ids to read those items' whole text); without, the project's latest, one line each.

{{- define "tools/session/pull_requests/project" -}}
{{template "tools/session/findings/project"}}
{{- end}}

{{- define "tools/session/pull_requests/task" -}}
{{template "tools/session/findings/task"}}
{{- end}}

{{- define "tools/session/pull_requests/ids" -}}
{{template "tools/session/findings/ids"}}
{{- end}}

{{- define "tools/session/pull_requests/feedback" -}}
with task, feedback items to read in full (at most 20), by the id its list gives
{{- end}}
