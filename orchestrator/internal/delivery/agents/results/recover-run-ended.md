This recovery has already ended, {{.Status}}{{with .Error}}: {{.}}{{end}}; you were woken when it did, and are not woken again.
{{- if ne .Status "done"}} It pushed nothing: to try again, ask with another branch under {{.Prefix}}.
{{- else}} Its commits are on the branch; the branch is not the task's: start a writer to merge it.
{{- end}}
