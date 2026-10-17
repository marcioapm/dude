## Feedback from people

People reviewing this task left these. Each is in its author's own words.{{range .Items}}

### {{.ID}} — {{.Author}}{{if .Anchor.Where}}

On `{{.Anchor.Where}}`{{if eq .Anchor.Kind "code"}}{{if .Anchor.HeadSHA}} at commit {{short .Anchor.HeadSHA}}{{else}}, uncommitted work of Run {{.Anchor.RunID}}{{end}}{{if .Outdated}} (those lines have changed since){{end}}{{end}}:{{with .Anchor.Excerpt}}

```diff{{range .}}
{{.Kind}}{{.Text}}{{end}}
```{{end}}{{with .Anchor.PageLine}}

{{.}}{{end}}{{if and (eq .Anchor.Kind "preview") .Picture}}

Picture: {{.Picture}}, attached (the element, outlined){{end}}{{if and (eq .Anchor.Kind "preview") .Anchor.Text}}

The element's text: {{clip (oneLine .Anchor.Text) 400}}{{end}}{{with .Anchor.Component}}{{if .File}}

Component {{.Name}} at `{{.File}}:{{.Line}}`{{end}}{{end}}{{end}}

{{quote .Body}}{{range .History}}

{{.Kind}} ({{.By}}): {{quote .Body}}{{end}}{{end}}

{{if .CLI -}}
Handle every item; none may be left unanswered. Fix what it asks, commit, then `dude feedback resolve ID --commit HEAD`. If you will not change it — it is wrong, or out of scope — say why with `dude feedback reply ID "…"`. `dude feedback` lists what is still open.
{{- else -}}
Handle every item; none may be left unanswered: fix it and resolve it with your commit (feedback_resolve), or reply saying why you will not (feedback_reply).
{{- end}}
