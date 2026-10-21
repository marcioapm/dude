## Review findings{{range .}}

### [{{.Severity}}] {{.Title}}{{if .File}}

`{{.File}}{{if gt .Line 0}}:{{.Line}}{{end}}`{{end}}{{with .Description}}

{{.}}{{end}}{{with .Acceptance}}

Done when: {{.}}{{end}}{{with .SuggestedFix}}

Suggested: {{.}}{{end}}{{end}}
