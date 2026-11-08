Read a failed check's bounded failure excerpt, or what its check run reported when it is not a GitHub Actions job or GitHub cannot provide its log. The excerpt can retain failure sites from the middle of the log as well as its end; omission markers mean intervening log text was left out, not that no other failures occurred. Use the check id from github_pull_request before deciding whether to rerun or fix it. Read only; the result is marked as external text. External-text marking is not secret redaction: this tool does not load credentials to redact returned logs; caller-supplied known-secret redaction is separate.

{{- define "tools/github_check_log/repo" -}}
the repository, owner/name, one of your projects'
{{- end}}

{{- define "tools/github_check_log/checkId" -}}
the check id from github_pull_request
{{- end}}
