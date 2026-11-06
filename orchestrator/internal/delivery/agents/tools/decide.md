Take the decision the delivery waits on: next (what Deliver would do now), ask_person (a question for the person: the note, with choices; before the pull request, always the fixed question Open / Draft / Show me the diff / Another round, the note its context, Open recommended unless you recommend another), wait (at pull request feedback: leave it), or open_pull_request — accepted only at the decision before the pull request, after the person answered Open or Draft to that question at the task's current head, or picked a choice with action open or draft on your earlier question in this delivery that still holds (the same attempt, nothing failing that they were not asked about, no commits since but the simplifier's). The result and the ledger cite that answer. Never open without a person's answer; do not ask again when it still holds.

{{- define "tools/decide/action" -}}
next, ask_person, wait or open_pull_request
{{- end}}

{{- define "tools/decide/note" -}}
why; for ask_person, the question (before the pull request, its context)
{{- end}}

{{- define "tools/decide/choices" -}}
ask_person away from the pull request's decision: 2 to 6 answers to offer, never as prose in the note; none for a question answered in the person's own words. Before the pull request the choices are fixed: leave this out
{{- end}}

{{- define "tools/decide/choices.label" -}}
the answer, short (at most 120 characters)
{{- end}}

{{- define "tools/decide/choices.description" -}}
one line on what it means or why (at most 300 characters)
{{- end}}

{{- define "tools/decide/choices.action" -}}
open or draft: picking it authorizes open_pull_request (as a draft for draft) at the decision before the pull request, while it holds; none for any other answer
{{- end}}

{{- define "tools/decide/choices.recommended" -}}
the one you would pick, at most one
{{- end}}

{{- define "tools/decide/recommend" -}}
ask_person: the label of the choice you recommend. Before the pull request one of the fixed choices (Open, Draft, Show me the diff or Another round; Open when left out); elsewhere one of your choices (or mark it recommended)
{{- end}}
