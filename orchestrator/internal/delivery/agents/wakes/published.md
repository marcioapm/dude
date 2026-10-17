{{if .NotMoved -}}
Your publish {{.ID}} is on the task branch in part: published {{join .Commits " "}}; {{join .NotMoved "; "}}. Run a review before the pull request.
{{- else -}}
Your publish {{.ID}} is on the task branch: {{join .Commits " "}}, {{.Lines}} lines in {{.Files}} files. Run a review before the pull request.
{{- end}}
