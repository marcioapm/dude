{{.Title}}{{with .Goal}}

{{.}}{{end}}{{with .Criteria}}

Acceptance criteria:
{{criteria .}}{{end}}{{with .Decisions}}

Decided by people during this work (part of the task; do not ask again):{{range .}}
- Q: {{oneLine .Question}}
  A: {{oneLine .Answer}}{{end}}{{end}}
