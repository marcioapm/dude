{{define "refusals/domain/escalations/task-not-found"}}task %s not found{{end}}

{{- define "refusals/domain/escalations/delivery-task-not-waiting-for-decision"}}delivery of task %s is not waiting for a decision{{end}}

{{- define "refusals/domain/escalations/not-way-go-on-after-one"}}%q is not a way to go on after %s; one of %s{{end}}

{{- define "refusals/domain/escalations/needs-note-say-why-task-may"}}%s needs a note: say why the task may ship with %s{{end}}

{{- define "refusals/domain/escalations/what-stopped-no-longer-kept-resume"}}what stopped is no longer kept to resume; try again or start over{{end}}

{{- define "refusals/domain/escalations/scope-only-for-checks-waiver"}}scope %q is not one this decision takes: "run", or "attempt" only when waiving unverified checks{{end}}

{{- define "refusals/domain/escalations/nothing-to-carry-in-attempt"}}nothing in the held report is a reported failure a later Run could repeat; waive it for this Run only{{end}}
