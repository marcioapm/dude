dude woke you; {{if eq .Count 1}}one thing:{{else}}{{.Count}} things:{{end}}
{{- range .Lines}}
- {{.}}
{{- end}}
{{- with .More}}
- … and {{.}} more
{{- end}}

Read what you need with your tools; take the decision waiting, or tell the person what you propose.
