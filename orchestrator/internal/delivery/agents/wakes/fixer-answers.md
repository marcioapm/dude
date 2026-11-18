{{if .Withheld -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: {{.Withheld}}. Its fix {{join .CommitList " "}} is not there; nothing it said was posted or resolved. Read its report (run_chat {{.RunID}}) and answer the comments yourself once a fix is on the pull request.
{{- else -}}
Fixer {{.RunID}}'s fix {{join .CommitList " "}} is on the pull request's head, so dude posted its answers there:{{with .Posted}} replied to {{join . ", "}}{{end}}{{with .Resolved}}{{if $.Posted}};{{end}} resolved {{join . ", "}}{{end}}{{with .Failed}}{{if or $.Posted $.Resolved}};{{end}} failed: {{join . "; "}}{{end}}. Add to or correct them with reply_on_pull_request if they say more than the fix did.
{{- end}}
