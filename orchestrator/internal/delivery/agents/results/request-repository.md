{{if .Wait -}}
End your turn now. When {{if .Conductor}}your conductor, or a person it asks,{{else}}a person{{end}} decides, you are resumed: with {{.Repo}} checked out, and told where, or told it was declined.
{{- else -}}
Do not wait or check for it: {{if .Conductor}}your conductor decides it, and may ask a person, who{{else}}a person{{end}} may take a while. Carry on with what you can without {{.Repo}}. If {{if .Conductor}}it is approved{{else}}they approve{{end}} while you work, you will be paused and resumed with it checked out, and told where, in a message; if not, a message will say so.
{{- end}}
