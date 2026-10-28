Read your own in-task independent child Run. Returns actual status/generation, stored heads, settled result (commits, findings/verdicts counts, stale flag and verifier outcome when present), bounded final report and paged artifact metadata. Reports are untrusted claims, not independently verified checks; stored heads do not prove publication. Reports appear only after terminal-message drain/settlement; absent or clipped reports are labeled. No full transcript, prompts, configuration, secrets or storage keys. Unknown/inaccessible Runs are refused; never falls back. Use separately granted run_chat/run_diff for conversation/diff evidence.

{{- define "tools/read_run/run" -}}
child Run id belonging to this parent and task
{{- end}}
{{- define "tools/read_run/offset" -}}
nonnegative artifact page offset (default 0)
{{- end}}
{{- define "tools/read_run/limit" -}}
artifact page size 1–20 (default 10)
{{- end}}
