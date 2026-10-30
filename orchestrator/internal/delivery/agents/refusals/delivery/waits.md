{{define "refusals/delivery/waits/deadline-required"}}deadline is required: a duration (45m, 2h) or a time, from 1 minute to 24 hours ahead; with no conditions the wait is a timer{{end}}

{{- define "refusals/delivery/waits/deadline-unreadable"}}%q is not a deadline: give a duration (45m, 2h) or an RFC 3339 time{{end}}

{{- define "refusals/delivery/waits/deadline-out-of-range"}}deadline %q is out of range: from 1 minute to 24 hours ahead. A longer wait is waited again when you are woken{{end}}

{{- define "refusals/delivery/waits/wake-all-or-any"}}wake %q is not all or any{{end}}

{{- define "refusals/delivery/waits/note-too-long"}}the note is at most %d characters{{end}}

{{- define "refusals/delivery/waits/too-many-conditions"}}at most %d conditions to a wait{{end}}

{{- define "refusals/delivery/waits/one-kind-per-condition"}}each condition is exactly one of runsEnded or prChecksFinal{{end}}

{{- define "refusals/delivery/waits/runs-count"}}runsEnded names 1 to %d Runs{{end}}

{{- define "refusals/delivery/waits/not-run-of-this-task"}}%s is not a Run of this task: only this task's Runs are waited on{{end}}

{{- define "refusals/delivery/waits/head-not-full-sha"}}head %q is not a full commit SHA: give all 40 (or 64) characters, or leave it out for the pull request's head now{{end}}

{{- define "refusals/delivery/waits/not-pull-request-of-this-task"}}%s is not a pull request of this task (pull_requests lists them){{end}}
