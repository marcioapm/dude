## Questions reviewers asked

Reviewers of your category asked these of a person earlier in this work. An answer is a fact to judge by.{{range $i, $q := .}}

**Q{{add1 $i}}** {{$q.Question}}{{with $q.FindingTitle}} (about: {{.}}){{end}}
{{if eq $q.Status "answered" -}}
Answered by {{if $q.ByPerson}}a person{{else}}the task's conductor{{end}}: {{$q.Answer}}
{{- else -}}
Not answered yet: judge without it; do not ask it again.
{{- end}}{{end}}
