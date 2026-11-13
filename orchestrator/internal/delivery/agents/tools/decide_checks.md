Decide a writer's checks report the checks gate asks you about (a checks wake names the Run and turn). Give one verdict per reported check, in the report's order and by its exact command, each with its reason: backed or unbacked for a pass; for a fail, preexisting when the Run's own calls, or your verify at the task's base commit, show it failing the same there (refused when the report has no base), same_as_waived with the waiver's waiverCursor when it is the failure a person waived for the attempt, else reject; for a not-run check, deferred when a later phase of this delivery runs it, else reject. A deferred check is a promise: once the pull request is open and its CI has reported, a second checks wake asks about it again, naming its ask, where only backed (citing the evidence at the ask's heads: a dispatch id and its outcome, a verify result, or the CI at the head) or reject is a verdict; answer with that ask, and an answer to an ask a newer wake superseded is refused. The pull request is not ready until you answer, and unanswered by the deadline it goes to a person. Then missing: none, or missing when a check obviously covering the change is absent. Establish a pre-existing failure with verify at the base commit, not by reading. Only the turn a checks wake names while it waits on you is accepted (the Run's latest; a second checks wake names the turn that deferred); nothing is pushed until you decide, and an undecided report is rejected or passes unverified after the wait.

{{- define "tools/decide_checks/runId" -}}
the writer Run whose report the checks wake names
{{- end}}

{{- define "tools/decide_checks/turn" -}}
the turn the checks wake names: the Run's latest
{{- end}}

{{- define "tools/decide_checks/checks" -}}
one verdict per reported check, in the report's order
{{- end}}

{{- define "tools/decide_checks/checks.command" -}}
the check's command exactly as reported
{{- end}}

{{- define "tools/decide_checks/checks.verdict" -}}
backed, unbacked, same_as_waived, preexisting, deferred or reject
{{- end}}

{{- define "tools/decide_checks/checks.reason" -}}
the evidence the verdict rests on
{{- end}}

{{- define "tools/decide_checks/checks.waiverCursor" -}}
for same_as_waived: the waiver's cursor the checks wake names
{{- end}}

{{- define "tools/decide_checks/ask" -}}
the ask a second checks wake names, for a deferred check; leave it out for a first checks wake
{{- end}}

{{- define "tools/decide_checks/missing" -}}
whether a check is obviously missing from the report
{{- end}}

{{- define "tools/decide_checks/missing.verdict" -}}
none or missing
{{- end}}

{{- define "tools/decide_checks/missing.reason" -}}
why
{{- end}}
