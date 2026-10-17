{{define "refusals/tools/server/arguments"}}arguments: %v{{end}}

{{- define "refusals/tools/server/too-many-calls-at-most-minute"}}too many calls: at most %d a minute; slow down{{end}}

{{- define "refusals/tools/server/limited-calls-in-run-this-run"}}%s is limited to %d calls in a run; this run has used them{{end}}
