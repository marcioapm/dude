This recovery has already ended, {{.Status}}{{with .Error}}: {{.}}{{end}}; how it ended reaches you once, by its own wake, if it has not already.
{{- if eq .Status "refused"}} It pushed nothing: a person must remove the secret from the Run's commits; recover_run refuses this Run until then.
{{- else if ne .Status "done"}} It pushed nothing: to try again, ask with another branch under {{.Prefix}}.
{{- else}} Its commits are on the branch; the branch is not the task's: start a writer to merge it.
{{- end}}
