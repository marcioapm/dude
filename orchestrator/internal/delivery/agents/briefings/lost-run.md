{{if .Failed -}}
dude here. Your predecessor's Run ({{.Old}}) failed{{with .Error}} ({{.}}){{end}}{{if .Prompt}} while it waited on {{.Name}} to answer:
{{printf "%q" .Prompt}}{{with .Choices}} (choices: {{join . " / "}}){{end}}.
{{- if .Reasked}}
dude asked {{.Name}} again for you (question {{.Reasked}}). End your turn; the answer is your next message. Do not ask again.
{{- else if .NotReasked}}
dude did not ask it again: {{.NotReasked}}. Decide what comes next with the conversation below.
{{- end}}{{else}}.{{end}}
The conversation so far is below.
{{- else -}}
dude here. lux lost your predecessor's Run ({{.Old}}) while it waited for {{.Name}} to answer:
{{printf "%q" .Prompt}}{{with .Choices}} (choices: {{join . " / "}}){{end}}.
The conversation so far is below. {{.Name}}'s answer is your next message: act on it as your predecessor would have, and do not ask again.
{{- end}}
