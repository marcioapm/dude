Your {{.Phase}} Run {{.RunID}} ended turn {{.Turn}} with a checks report; nothing is pushed until you decide it with decide_checks (runId {{.RunID}}, turn {{.Turn}}), one verdict per check in this order, each with its reason.
{{- with .Bases}} The task's bases: {{.}}.{{end}}
{{- range .Waivers}} Check {{.N}}, {{printf "%q" .Command}}, may be the failure the {{.Role}}'s waiver accepted: same_as_waived cites waiverCursor {{.Cursor}}.{{end}}
{{- range .Checks}} {{printf "%q" .Command}} reported {{.Result}} (evidence {{.Evidence}}):
{{- if eq .Result "pass"}} is the pass backed by the Run's recorded calls{{with .Call}} (dude recorded its latest call of it: {{.}}){{end}} (backed or unbacked)?
{{- else if eq .Result "fail"}}{{if .Waiver}} is it the same failure as that waiver's (same_as_waived)?{{end}} is it pre-existing at the task's base{{with .Call}} (dude recorded its latest call of it at the base: {{.}}){{end}}, shown failing the same there (preexisting), or else reject?
{{- else}} is it deferred to a later phase{{if $.TestPhase}} (this delivery runs a test phase){{else}} (this delivery runs no test phase){{end}} (deferred), or else reject?
{{- end}}
{{- end}} Then missing: is a check covering this change obviously missing (none or missing)?
