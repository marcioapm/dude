{{define "refusals/delivery/waits/deadline-required"}}deadline is required: a duration (45m, 2h) or a time, from 1 minute to 24 hours ahead; with no conditions the wait is a timer{{end}}

{{- define "refusals/delivery/waits/deadline-unreadable"}}%q is not a deadline: give a duration (45m, 2h) or an RFC 3339 time{{end}}

{{- define "refusals/delivery/waits/deadline-out-of-range"}}deadline %q is out of range: from 1 minute to 24 hours ahead. A longer wait is waited again when you are woken{{end}}

{{- define "refusals/delivery/waits/wake-all-or-any"}}wake %q is not all or any{{end}}

{{- define "refusals/delivery/waits/note-too-long"}}the note is at most %d characters{{end}}

{{- define "refusals/delivery/waits/too-many-conditions"}}at most %d conditions to a wait{{end}}

{{- define "refusals/delivery/waits/one-kind-per-condition"}}each condition is exactly one of runsEnded, prChecksFinal, workflowEnded, after or siblingTask{{end}}

{{- define "refusals/delivery/waits/runs-count"}}runsEnded names 1 to %d Runs{{end}}

{{- define "refusals/delivery/waits/not-run-of-this-task"}}%s is not a Run of this task: only this task's Runs are waited on{{end}}

{{- define "refusals/delivery/waits/head-not-full-sha"}}head %q is not a full commit SHA: give all 40 (or 64) characters, or leave it out for the pull request's head now{{end}}

{{- define "refusals/delivery/waits/not-pull-request-of-this-task"}}%s is not a pull request of this task (pull_requests lists them){{end}}

{{- define "refusals/delivery/waits/not-dispatch-of-this-task"}}%s is not a workflow this task dispatched (run_workflow's dispatchId; pull_requests lists them under dispatches){{end}}

{{- define "refusals/delivery/waits/dispatch-failed"}}GitHub refused dispatch %s (%s): nothing ran, so there is nothing to wait for. Fix what GitHub refused, then dispatch it again (run_workflow){{end}}

{{- define "refusals/delivery/waits/after-unreadable"}}after %q is not a duration: give one such as 45m or 2h{{end}}

{{- define "refusals/delivery/waits/after-too-short"}}after %q is under 30m: for a shorter time, just set the deadline{{end}}

{{- define "refusals/delivery/waits/after-past-deadline"}}after %q comes at or after the wait's deadline: the deadline comes first; just set the deadline{{end}}

{{- define "refusals/delivery/waits/sibling-not-found"}}%q is not a task of this organisation: name a task on this task's epic by its key (CP-62){{end}}

{{- define "refusals/delivery/waits/sibling-is-this-task"}}siblingTask names this task: only another task in the same epic is waited on{{end}}

{{- define "refusals/delivery/waits/sibling-not-on-this-epic"}}%s is not on this task's epic: only a task in the same epic is waited on{{end}}

{{- define "refusals/delivery/waits/sibling-becomes-unknown"}}becomes %q is not one of %s; leave becomes out for any change{{end}}
