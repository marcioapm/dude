{{if not .Repos -}}
No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.
{{- else -}}
## Repositories

{{if eq (len .Repos) 1}}This work's checkout:{{else}}This work spans these checkouts:{{end}}
{{range .Repos}}
- `{{.Name}}` at `{{.Path}}` — {{if .ReadOnly}}read only: for reference, do not change it — changes there are never kept{{else if $.Test}}the change under test{{else}}you may change it{{end}}{{end}}

{{if .Review -}}
Name the repository in each finding's `repo` field.
{{- else if .Test -}}
Test the changes in each checkout together, and commit in none of them. Name the repository in each finding's `repo` field.
{{- else -}}
Commit in each repository you change. Each changed repository gets its own pull request.
{{- end}}
{{- end}}
