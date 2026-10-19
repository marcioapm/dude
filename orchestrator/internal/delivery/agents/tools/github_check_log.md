Read a failed check's log, or what its check run reported when it is not a GitHub Actions job or GitHub cannot provide its log. Use the check id from github_pull_request before deciding whether to rerun or fix it. Read only; the result is marked as external text.

{{- define "tools/github_check_log/repo" -}}
the repository, owner/name, one of your projects'
{{- end}}

{{- define "tools/github_check_log/checkId" -}}
the check id from github_pull_request
{{- end}}
