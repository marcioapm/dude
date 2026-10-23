{{with .Repo}}in **{{.}}** — {{end}}{{with .Path}}`{{.}}` — {{end}}{{with .Author}}**{{.}}** — {{end}}{{.Body}}{{if and .ID .Tools}} _({{.ID}})_{{end}}{{range .Checks}}

**Failing check: {{.Name}}**{{with .URL}} ({{.}}){{end}}{{with .Unread}}

dude could not read this job's log ({{.}}), so reproduce the failure locally from the workflow file that runs it, under `.github/workflows/`.{{end}}{{with .Log}}

```
{{fenced .}}
```{{end}}{{end}}
