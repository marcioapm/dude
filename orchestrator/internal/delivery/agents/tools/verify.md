Get a commit verified without changing it: dude starts a verifier — a report-only Run with publishing off, checking out the commit you name — that runs the commands you give, or the repository's tests for the paths you name (the change's tests with neither), and reports each command's exit code and the tail of its output. Its result wakes you as a message, passed only when every command exited 0, without taking a workflow decision; at any stage of the delivery, while it is live. The commit must be one GitHub has: the task branch's head (the default), a pull request's, or what you published. At most 10 a Run.

{{- define "tools/verify/repo" -}}
the repository to check out; the task's only one when left out
{{- end}}

{{- define "tools/verify/commit" -}}
the commit to verify, by its full SHA; the task branch's head in that repository when left out
{{- end}}

{{- define "tools/verify/commands" -}}
the commands to run from the repository's root, in order, one line each (at most 10)
{{- end}}

{{- define "tools/verify/paths" -}}
without commands: the paths whose tests to run, the verifier finding how the repository runs them (at most 50)
{{- end}}

{{- define "tools/verify/note" -}}
what else the verifier should know, as a Run you start is told
{{- end}}

{{- define "tools/verify/key" -}}
names the verification so a retry of the call is the same one; the call's own id when left out
{{- end}}
