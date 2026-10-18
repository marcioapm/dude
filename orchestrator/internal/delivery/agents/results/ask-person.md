{{if .Escalation -}}
End your turn now. The owner picking a choice decides the escalation, as the banner does; an answer in their own words is your next message, and lets you decide it with decide_escalation.
{{- else -}}
{{template "contracts/conductor-asked"}}
{{- end}}
