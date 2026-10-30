Steer a running Run of this task — a phase Run (implement, review, fix, simplify, test) or a child agent you started — as a person would: your words reach it at its next step, without stopping the work it is doing. Steer an agent going the wrong way, or to add something the person just said; start another phase only once a Run has ended. interrupt stops its turn so it hears this now: only when its current work is wasted. You are woken when it reads it, or if it never will. Allowed whoever takes the decisions; refused for a Run that ended, another task's, an earlier attempt's, or another conductor's child.

{{- define "tools/steer/run" -}}
the Run to steer (run_…): a phase Run of this task's current attempt, or your own child agent, still running
{{- end}}

{{- define "tools/steer/text" -}}
what to tell it: read at its next step, in the turn it is in
{{- end}}

{{- define "tools/steer/interrupt" -}}
stop its current turn so it hears this now; only when the work it is doing is wasted
{{- end}}

{{- define "tools/steer/feedbackIds" -}}
people's open feedback items (fbi_…) to pass to an implementer or fixer: dude adds their own words, never a summary
{{- end}}
