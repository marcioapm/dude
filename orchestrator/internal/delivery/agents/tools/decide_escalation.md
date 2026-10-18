Decide the escalation the delivery is stopped at (retry, accept, waive, resume, done, wait or stop, as it offers), with a note: only after the task's owner answered your question about this escalation in their own words, handing it to you; otherwise the person decides on the banner. A choice they pick decides it without you, except a waiver or an acceptance of a failed test, which is never a choice: it needs the person's own words. Allowed once per escalation.

{{- define "tools/decide_escalation/action" -}}
one of the escalation's actions: retry, accept, resume, done, wait or stop
{{- end}}

{{- define "tools/decide_escalation/note" -}}
what you decided and why, kept with the task for every agent from here on
{{- end}}
