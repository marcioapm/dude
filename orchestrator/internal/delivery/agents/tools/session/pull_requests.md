A linked project's pull requests: with task, that task's in full (checks, reviews, feedback); without, the project's latest, one line each.

{{- define "tools/session/pull_requests/project" -}}
{{template "tools/session/findings/project"}}
{{- end}}

{{- define "tools/session/pull_requests/task" -}}
{{template "tools/session/findings/task"}}
{{- end}}

{{- define "tools/session/pull_requests/ids" -}}
{{template "tools/session/findings/ids"}}
{{- end}}
