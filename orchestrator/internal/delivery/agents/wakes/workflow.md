{{- if eq .Outcome "completed"}}Workflow {{.Workflow}} you dispatched ({{.ID}}) on {{.Repo}} at {{.Ref}} completed: {{or .Conclusion "no conclusion"}}, attempt {{.Attempt}}{{with .URL}} ({{.}}){{end}}.
{{- else if eq .Outcome "unknown"}}Workflow {{.Workflow}} you dispatched ({{.ID}}) on {{.Repo}} at {{.Ref}} is unknown: {{.Reason}}. Investigate (github_branch_runs); it is neither a success nor a failure.
{{- else}}dude cannot see this workflow run: {{.Workflow}} you dispatched ({{.ID}}) on {{.Repo}} at {{.Ref}}{{with .RunID}}, run {{.}}{{end}}; {{.FailedRead}} reads in a row failed since {{.Failing}}: {{clip .LastError 200}}.
{{- with .Seen}} Last seen {{$.Status}} at {{.}}.{{end}} This is not "not finished": investigate; dude keeps reading and wakes you again when it can see it once more and it ends.
{{- end}}
