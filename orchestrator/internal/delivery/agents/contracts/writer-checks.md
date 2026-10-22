## What the writers before you checked

These are the checks the implementer, fixers and simplifier of this attempt reported, as dude recorded them, oldest first, each with the commits that Run published. They were run on those commits, not necessarily on the head you test: a check that passed there says nothing about a later commit. Use them to know which failures are already known and accepted, not as evidence for any criterion, and do not re-run them as routine.
{{range .}}
- {{.Role}}{{with .Heads}} ({{.}}){{end}}: {{.Status}}{{with .Message}}: {{.}}{{end}}
{{- range .Checks}}
  - {{.Command}}: {{.Result}}{{with .Reason}} ({{.}}){{end}}
{{- end}}
{{- end}}
