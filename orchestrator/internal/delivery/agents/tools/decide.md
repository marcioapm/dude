Take the decision the delivery waits on: next (what Deliver would do now), ask_person (a question for the person in the note; before the pull request, the fixed question Open / Draft / Show me the diff / Another round), wait (at pull request feedback: leave it), or open_pull_request — accepted only after the person answered Open or Draft to that question, at the task's current head.

{{- define "tools/decide/action" -}}
next, ask_person, wait or open_pull_request
{{- end}}

{{- define "tools/decide/note" -}}
why; for ask_person, the question
{{- end}}
