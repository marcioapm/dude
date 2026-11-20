Your {{.Phase}} Run {{.RunID}} ended turn {{.Turn}} with a checks report; nothing is pushed until you decide it with decide_checks (runId {{.RunID}}, turn {{.Turn}}), one verdict per check in this order, each with its reason.
{{- with .Bases}} The task's bases: {{.}}.{{end}}
{{- range .Waivers}} Check {{.N}}, {{printf "%q" .Command}}, may be the failure the {{.Role}}'s waiver accepted: same_as_waived cites waiverCursor {{.Cursor}}.{{end}}
{{- with .Evidence}}
{{- if .Read}} Evidence: the Run's latest commit is {{.Heads}}, tree {{if .Clean}}clean{{else}}dirty{{end}} at report.
{{- if and .Calls .Dropped}} {{.Dropped}} older calls did not fit here: run_calls lists them.
{{- else if .Dropped}} Its shell calls started after that commit was made did not fit here, {{.Dropped}} of them{{if .More}} (of the latest {{.Max}}){{end}}: run_calls lists them.
{{- else if not .Calls}} No shell call was recorded after that commit was made, by the Run or its subagents: a pass is backed only by a call at the latest commit.
{{- end}}
{{- else}} Evidence: dude could not read the Run's checkouts, so no call is shown; read the Run's calls with run_calls.
{{- end}}
{{- end}}
{{- range .Checks}} {{printf "%q" .Command}} reported {{.Result}} (evidence {{.Evidence}}):
{{- if eq .Result "pass"}} is the pass backed by a call below that ran it at the latest commit and succeeded (backed or unbacked)?
{{- else if eq .Result "fail"}}{{if .Waiver}} is it the same failure as that waiver's (same_as_waived)?{{end}} is it pre-existing at the task's base, shown failing the same there (preexisting), or else reject?
{{- else}} is it deferred to a later phase{{if $.TestPhase}} (this delivery runs a test phase){{else}} (this delivery runs no test phase){{end}} (deferred), or else reject?
{{- end}}
{{- end}} Then missing: is a check covering this change obviously missing (none or missing)?
{{- with .Evidence}}{{if and .Read .Calls}} Its shell calls started after that commit was made, newest first{{if .More}} (the latest {{.Max}}; older ones by run_calls){{end}}; you decide which call, if any, ran each reported pass, however its command is written (a directory, an environment, a pipe, a subagent):
{{- range .Calls}} [{{.N}}] {{.Command}} → {{.Exit}}{{with .Child}}, by subagent {{.}}{{end}}{{with .Tail}}, output ends {{.}}{{end}}.
{{- end}}
{{- end}}
{{- end}}
