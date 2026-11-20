The shell calls an implementer, fixer or simplifier Run of your task, and its subagents, started after a commit was made, newest first, at most 30: each call's command, the exit code its agent's harness recorded (or "no exit recorded"), its output's tail, and the subagent that made it. It is the evidence a checks wake shows, for any commit: decide from it whether a reported pass was run at the latest commit and succeeded, whatever shape its command took. With commit left out, the Run's HEAD, and whether its working tree is clean. Read in the Run's container; with the container stopped, the HEAD its latest checks ask read.

{{- define "tools/run_calls/run" -}}
a writer Run of your task's current attempt (run_…); empty is the latest one
{{- end}}

{{- define "tools/run_calls/commit" -}}
a commit's SHA, 7 to 40 hexadecimal characters: the calls started after it was made; empty is the Run's HEAD
{{- end}}

{{- define "tools/run_calls/filter" -}}
only calls whose command holds this text, any case (for example a test runner's name); empty lists every call
{{- end}}
