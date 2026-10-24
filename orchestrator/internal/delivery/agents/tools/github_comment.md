Post a conversation comment on an issue or pull request in a repository covered by this automation, as the dude GitHub App. Omit repo and number to use the triggering issue or pull request.

{{- define "tools/github_comment/repo" -}}
repository, owner/name; defaults to the triggering repository
{{- end}}

{{- define "tools/github_comment/number" -}}
issue or pull request number; defaults to the triggering issue or pull request
{{- end}}

{{- define "tools/github_comment/body" -}}
the comment text, between 1 and 65536 bytes
{{- end}}
