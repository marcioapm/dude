{{define "refusals/delivery/limit-requests/unknown-limit"}}no limit %q to raise: name one of %s{{end}}

{{- define "refusals/delivery/limit-requests/reason-required"}}say why the task needs it (reason): the person decides on it{{end}}

{{- define "refusals/delivery/limit-requests/reason-too-long"}}a reason of at most %d characters{{end}}

{{- define "refusals/delivery/limit-requests/no-delivery"}}this task has no delivery in progress: there is no limit of it to raise{{end}}

{{- define "refusals/delivery/limit-requests/task-ended"}}this task is %s: there is no limit of it to raise{{end}}

{{- define "refusals/delivery/limit-requests/already-open"}}a request to raise %s is waiting on its answer already (%s): wait for it{{end}}

{{- define "refusals/delivery/limit-requests/question-open"}}you already have a question waiting for a person: wait for its answer, then ask{{end}}

{{- define "refusals/delivery/limit-requests/no-bound"}}the organisation's fix rounds per pull request are not bounded (0): nothing to raise{{end}}

{{- define "refusals/delivery/limit-requests/to-out-of-range"}}the limit is %d now: ask for %d to %d (to){{end}}

{{- define "refusals/delivery/limit-requests/rerun-left"}}the failed checks on %s#%d may still be run again on this head: rerun_failed_checks, no request needed{{end}}

{{- define "refusals/delivery/limit-requests/no-pull-request"}}no open pull request of this task by that name: a rerun is of an open pull request's head{{end}}

{{- define "refusals/delivery/limit-requests/name-pull-request"}}this task has several open pull requests: name the one (pr, as repo#number){{end}}

{{- define "refusals/delivery/limit-requests/no-head"}}dude has not read %s#%d's head yet: refresh_pull_requests, then ask again{{end}}
