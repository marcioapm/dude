Take the decision the delivery waits on by starting a phase from the task's head: implement, review (some or all categories), fix (some or all open findings; at pull request feedback, the feedback), simplify or test. The workflow creates the Runs as it creates its own, waits for them, and wakes you at the next decision. While the delivery waits on its pull requests with nothing running, fix (say what, in note), review or test start too, on the pull request's branch, and it waits on them again after. Refused when you do not take the task's decisions, while a Run or publish is changing the branch, or past the policy's bounds: then ask the person.

{{- define "tools/start_phase/phase" -}}
implement, review, fix, simplify or test
{{- end}}

{{- define "tools/start_phase/categories" -}}
review: the reviewers to run (correctness, tests, security, database, api, frontend, documentation, performance); none runs those the change warrants
{{- end}}

{{- define "tools/start_phase/findings" -}}
fix: the open findings to fix (fnd_…); none fixes every open one
{{- end}}

{{- define "tools/start_phase/note" -}}
what you ask of the Run, added to its prompt
{{- end}}
