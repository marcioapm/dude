Review round {{.Round}} done ({{.Range}}; {{join .Lenses ", "}}): {{.Open}} open at the fix bar{{with .Below}}, {{.}} below it{{end}}{{with .People}}, {{.}} people's feedback items (feedback){{end}}.
{{- range $i, $f := .Shown}}{{if $i}};{{end}} {{.ID}} [{{.Severity}}{{if .Trivial}}, trivial{{end}}{{if eq .Introduced "no"}}, pre-existing{{else if eq .Introduced "yes"}}, introduced{{end}}{{if .Unverified}}, unverified{{end}}{{if .Touches}}, on lines the fix changed{{end}}] {{.Where}}: {{clip (oneLine .Title) 80}}{{end}}{{if .Shown}}.{{end}}
{{- with .More}} And {{.}} more at the bar (findings){{with $.MoreTouch}}, {{.}} of them on lines the fix changed{{end}}.{{end}}
{{- with .Fix}} The fix before it: {{.}}{{end}}{{with .Change}} {{.}}{{end}}
{{- if .Long}} 8 rounds in this loop: consider asking the person.{{end}}
