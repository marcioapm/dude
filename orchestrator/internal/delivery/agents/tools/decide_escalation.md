Decide the escalation the delivery is stopped at (retry, accept, amend, waive, resume, done, wait or stop, as it offers), only after the owner answered your question about this escalation in their own words. Otherwise the person decides on the banner. Actions requiring a note cannot be offered as choices: stuck retry; not_converging retry, accept or amend; accepting a failed test; waiving a check. Use the person's own direction or reason, not permission to choose for them. Allowed once per escalation.

{{- define "tools/decide_escalation/action" -}}
one of the escalation's actions: retry, accept, amend, waive, resume, done, wait or stop; for not_converging, retry fixes differently, accept accepts only the named risks, and amend replaces the named findings' criteria with the person's words before fixing
{{- end}}

{{- define "tools/decide_escalation/note" -}}
what you decided and why, kept with the task for later agents; required-note actions need the person's own direction or reason in their answer or this note; amend uses their words as the replacement criterion
{{- end}}
