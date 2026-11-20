{{if eq .Cause "ended" -}}
Fixer {{.RunID}}'s delivery ended with its answers held: {{or .Ended .Withheld}}.{{with .WithheldList}} Withheld, never posted or resolved: {{join . ", "}}.{{end}}{{with .InFlight}} Being posted as it ended, so not withheld: {{join . ", "}}; dude tells you what each ends as.{{end}}{{if not (or .WithheldList .InFlight)}} Nothing it said was posted or resolved.{{end}} Read its report (run_chat {{.RunID}}) if the comments still need an answer.
{{- else if .Withheld -}}
{{if eq .Cause "unverified" -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: dude {{.Withheld}}. Its fix {{join .CommitList " "}} may be there; nothing it said was posted or resolved. Read its report (run_chat {{.RunID}}) and answer the comments yourself if the fix is on the pull request.
{{- else -}}
Fixer {{.RunID}}'s answers on the pull request were not posted: {{.Withheld}}. Its fix {{join .CommitList " "}} is not there; nothing it said was posted or resolved. Read its report (run_chat {{.RunID}}) and answer the comments yourself once a fix is on the pull request.
{{- end}}
{{- else -}}
Fixer {{.RunID}}'s fix {{join .CommitList " "}} is on the pull request, so dude posted its answers there:{{with .Posted}} replied to {{join . ", "}}{{end}}{{with .Resolved}}{{if $.Posted}};{{end}} resolved {{join . ", "}}{{end}}{{with .Failed}}{{if or $.Posted $.Resolved}};{{end}} failed: {{join . "; "}}{{end}}. Add to or correct them with reply_on_pull_request if they say more than the fix did.
{{- end}}

{{- define "wakes/fixer-answers/reconciled"}}Fixer {{.RunID}}'s answer {{.Label}}, being posted when its delivery ended ({{.Ended}}),
{{- if eq .Outcome "posted"}} was posted: it is on the pull request.
{{- else if eq .Outcome "failed"}} failed: GitHub did not take it; it is not on the pull request.
{{- else if eq .Outcome "withheld"}} was not posted: the delivery ended before it was posted.
{{- else}} is uncertain: it may have been posted; check the pull request. dude could not tell whether GitHub took it{{with .Detail}} ({{clip . 200}}){{end}}.
{{- end}}{{end}}
