Ask people for a review of one of your task's pull requests, on GitHub, as dude. Only the repository's collaborators: anyone else is refused and named. Refused for another task's pull request.

{{- define "tools/request_review/pr" -}}
the pull request: its number, or repo#number, one of your task's
{{- end}}

{{- define "tools/request_review/logins" -}}
GitHub logins to ask for a review; collaborators on the repository only
{{- end}}
