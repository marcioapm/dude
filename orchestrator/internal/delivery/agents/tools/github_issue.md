Read an issue of one of your projects' GitHub repositories: its title, body, author, state and labels, and its comments in order (the first 50). Read only; what people wrote comes marked as external text. A pull request's conversation is read with github_pull_request.

{{- define "tools/github_issue/repo" -}}
the repository, owner/name, one of your projects'
{{- end}}

{{- define "tools/github_issue/number" -}}
the issue's number
{{- end}}
