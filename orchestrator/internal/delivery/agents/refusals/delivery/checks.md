{{define "refusals/delivery/checks/run-not-of-task"}}that Run is not a writer Run of this task's current attempt: decide only the checks reports your task's Runs ask you about{{end}}
{{define "refusals/delivery/checks/not-latest-turn"}}that is not the Run's latest turn: its latest is %d, and a verdict on an earlier turn is never read. Decide the turn the latest checks wake names{{end}}
{{define "refusals/delivery/checks/not-waiting"}}the checks gate is not waiting on you for that Run's turn: it was decided already, or it was never asked of you{{end}}
{{define "refusals/delivery/checks/incomplete"}}decide every reported check, in the report's order and by its exact command, one verdict each: %s{{end}}
{{define "refusals/delivery/checks/needs-reason"}}every verdict needs its reason: %q has none{{end}}
{{define "refusals/delivery/checks/verdict-not-allowed"}}%q is not a verdict for %q, reported %s: use one of %s{{end}}
{{define "refusals/delivery/checks/no-base"}}preexisting needs a recorded commit of the target branch this task builds on, and this report has none: reject the failure instead{{end}}
{{define "refusals/delivery/checks/waiver-cursor"}}same_as_waived for %q must cite the waiver it matches: waiverCursor %d{{end}}
{{define "refusals/delivery/checks/missing"}}missing needs verdict none or missing, and a reason{{end}}
