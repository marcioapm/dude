## Project facts

Pinned on this project for every agent on it: hold to them. Each is a memory; its id reads the whole of it.
{{range .Facts}}
- **{{.Title}}** ({{.ID}}{{with .By}}, pinned by {{.}}{{end}}){{with .Text}}: {{.}}{{end}}{{end}}{{if .More}}

{{.More}} more {{if eq .More 1}}is{{else}}are{{end}} pinned than fit here: the project's memory search finds them.{{end}}
