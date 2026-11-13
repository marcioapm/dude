{{define "refusals/delivery/checks/run-not-of-task"}}that Run is not a writer Run of this task's current attempt: decide only the checks reports your task's Runs ask you about{{end}}
{{define "refusals/delivery/checks/not-latest-turn"}}that is not the Run's latest turn: its latest is %d, and a verdict on an earlier turn is never read. Decide the turn the latest checks wake names{{end}}
{{define "refusals/delivery/checks/not-waiting"}}the checks gate is not waiting on you for that Run's turn: it was decided already, or it was never asked of you{{end}}
{{define "refusals/delivery/checks/incomplete"}}decide every reported check, in the report's order and by its exact command, one verdict each: %s{{end}}
{{define "refusals/delivery/checks/needs-reason"}}every verdict needs its reason: %q has none{{end}}
{{define "refusals/delivery/checks/verdict-not-allowed"}}%q is not a verdict for %q, reported %s: use one of %s{{end}}
{{define "refusals/delivery/checks/no-base"}}preexisting needs a recorded commit of the target branch this task builds on, and this report has none: reject the failure instead{{end}}
{{define "refusals/delivery/checks/waiver-cursor"}}same_as_waived for %q must cite the waiver it matches: waiverCursor %d{{end}}
{{define "refusals/delivery/checks/missing"}}missing needs verdict none or missing, and a reason{{end}}
{{define "refusals/delivery/checks/reason-long"}}a reason is at most %d characters, and %s's is %d: say what decides the verdict, the Run's Chat keeps the rest{{end}}
{{define "refusals/delivery/checks/ask-missing"}}that turn's deferred checks are asked about again by a second checks wake: answer it with the ask that wake names (ask), so the answer counts at the heads it was asked at{{end}}
{{define "refusals/delivery/checks/no-such-ask"}}%q is not a second checks ask about that Run's turn: give the ask the second checks wake names, with its runId and turn{{end}}
{{define "refusals/delivery/checks/ask-superseded"}}ask %q is superseded: the pull requests' heads moved and a newer second checks wake asks again at the new heads. Evidence at the old heads keeps nothing there: answer the newest wake, with its ask, once the check is kept at its heads{{end}}
