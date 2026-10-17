{{if .Wait -}}
End your turn now. When a person decides, you are resumed: with {{.Repo}} checked out, and told where, or told it was declined.
{{- else -}}
Do not wait or check for it: a person may take a while. Carry on with what you can without {{.Repo}}. If they approve while you work, you will be paused and resumed with it checked out, and told where, in a message; if not, a message will say so.
{{- end}}
