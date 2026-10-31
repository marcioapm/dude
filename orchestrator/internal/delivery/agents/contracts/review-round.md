## Round {{.Round}}

{{if and (eq .Round 1) .Ranged -}}
This is the loop's first review round: review all of the range above.
{{- else if eq .Round 1 -}}
This is the loop's first review round: review the whole change.
{{- else if .FixRound -}}
This is review round {{.Round}}, of what changed since round {{.Previous}} reviewed the branch (the range above). Report what those commits introduce, and what they were sent to fix and left unfixed.
{{- else if .Ranged -}}
This is review round {{.Round}}: review the range above.
{{- else -}}
This is review round {{.Round}}, of the whole change as it is now: no narrower range since round {{.Previous}} could be named.
{{- end}}{{with .Decided}}

Decided in earlier rounds — do not raise these again, nor restate one in other words:{{range .}}
- {{.ID}} [{{.Severity}}] {{.Title}}: {{.How}}{{end}}{{end}}

The conductor triages every finding before anything is fixed. It asks of each: can it happen through a supported path — its `scenario` starts at a real entry point (a request, a command, a step of the workflow); and did this change introduce or worsen it (`introduced`). A pre-existing problem is still worth reporting, marked `introduced: no`: the conductor files it to be fixed later rather than here.
