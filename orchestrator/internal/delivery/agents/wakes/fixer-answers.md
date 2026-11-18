{{if .Withheld -}}
{{if eq .Cause "ended" -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: {{.Withheld}}. Nothing it said was posted or resolved; read its report (run_chat {{.RunID}}) if the comments still need an answer.
{{- else if eq .Cause "unverified" -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: dude {{.Withheld}}. Its fix {{join .CommitList " "}} may be there; nothing it said was posted or resolved. Read its report (run_chat {{.RunID}}) and answer the comments yourself if the fix is on the pull request.
{{- else -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: {{.Withheld}}. Its fix {{join .CommitList " "}} is not there; nothing it said was posted or resolved. Read its report (run_chat {{.RunID}}) and answer the comments yourself once a fix is on the pull request.
{{- end}}
{{- else -}}
Fixer {{.RunID}}'s fix {{join .CommitList " "}} is on the pull request, so dude posted its answers there:{{with .Posted}} replied to {{join . ", "}}{{end}}{{with .Resolved}}{{if $.Posted}};{{end}} resolved {{join . ", "}}{{end}}{{with .Failed}}{{if or $.Posted $.Resolved}};{{end}} failed: {{join . "; "}}{{end}}. Add to or correct them with reply_on_pull_request if they say more than the fix did.
{{- end}}
