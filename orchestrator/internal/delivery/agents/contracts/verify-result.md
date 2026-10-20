Your verification {{.Child}} of {{.Repo}}{{with .Commit}} at {{.}}{{end}} {{.Status}}
{{- if eq .Reason "no-report"}}: the verifier reported no results
{{- else if .Reason}}: the verifier's Run was {{.Reason}}
{{- else}}: {{.Passed}} of {{len .Results}} commands exited 0
{{- end}}. Nothing was published; this does not advance the delivery.
{{- range .Results}}
- exit {{.Exit}}: {{clip .Command 200}}
{{- end}}

{{.JSON}}
