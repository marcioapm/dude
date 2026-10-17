{{with .Repo}}in **{{.}}** — {{end}}{{with .Path}}`{{.}}` — {{end}}{{with .Author}}**{{.}}** — {{end}}{{.Body}}{{if and .ID .Tools}} _({{.ID}})_{{end}}{{range .Checks}}

**Failing check: {{.Name}}**{{with .URL}} ({{.}}){{end}}{{with .Log}}

```
{{fenced .}}
```{{end}}{{end}}
