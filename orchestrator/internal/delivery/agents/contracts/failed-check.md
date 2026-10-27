{{if .Child -}}
- A failed check or one that will not run is a problem to solve before you finish (see How you work), unless it fails the same at the commit you started from or is the tester's, as above. If you cannot solve it, report it here as `fail` or `not-run`, and say in your final reply what you ran, the exact error, and what you tried. Never list a check as `pass` that you did not see pass.
{{- else -}}
- A failed check or one that will not run is a problem to solve before you finish (see How you work), unless it fails the same at the commit you started from or is the tester's, as above. If you cannot, ask with `ask_person`: what you ran, the exact error, and what you tried, then end your turn to wait for the answer. Reporting `fail` or `not-run` here does not pause the Run. Never list a check as `pass` that you did not see pass.
{{- end}}
