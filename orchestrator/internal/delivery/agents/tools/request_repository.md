Ask for another of the project's repositories when this work needs it — to read code this depends on, or (an implementer) to change it too. A person decides. Carry on meanwhile if you can; if you cannot go on without it, ask with wait: true and end your turn — you are resumed with it, or told it was declined.

{{- define "tools/request_repository/repository" -}}
the repository's name, or owner/name, as the organization knows it
{{- end}}

{{- define "tools/request_repository/write" -}}
you need to change it (it gets its own pull request); otherwise read only
{{- end}}

{{- define "tools/request_repository/reason" -}}
why this work needs it, for the person deciding
{{- end}}

{{- define "tools/request_repository/wait" -}}
you cannot go on without it: end your turn after asking, and you are resumed when a person decides (otherwise carry on; a later phase gets it if approved)
{{- end}}
