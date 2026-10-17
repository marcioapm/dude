Messages to or from {{.Key}}:{{range .Messages}}
- {{.At}} {{.From}} → {{.To}} ({{.ID}}): {{printf "%q" .Text}}{{if .Reply}} Replies to {{.Reply}}.{{else if .Answered}} Answered {{.Answered}}.{{else}} No reply yet.{{end}}{{end}}{{if .More}}
- … and {{.More}} more: `dude task list --peers`{{end}}
