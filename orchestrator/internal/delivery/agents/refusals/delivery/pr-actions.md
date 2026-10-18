{{define "refusals/delivery/pr-actions/not-yours-do-task-conductor-acts"}}%s is not yours to do: a task's conductor acts on its pull requests{{end}}

{{- define "refusals/delivery/pr-actions/you-are-no-longer-this-task-4"}}you are no longer this task's conductor: another took over from you. Do nothing on GitHub{{end}}

{{- define "refusals/delivery/pr-actions/your-run-acts-on-nothing"}}your Run is %s: it acts on nothing{{end}}

{{- define "refusals/delivery/pr-actions/you-resolve-only-review-threads-in"}}you resolve only the review threads in your feedback, and it has none{{end}}

{{- define "refusals/delivery/pr-actions/not-pull-request-your-task-you"}}%s is not a pull request of your task: you act only on your task's (pull_requests lists them){{end}}

{{- define "refusals/delivery/pr-actions/there-nothing-do-on"}}%s#%d is %s: there is nothing to do on it{{end}}

{{- define "refusals/delivery/pr-actions/not-on-github-dude-connected"}}%s#%d is not on a GitHub dude is connected to{{end}}

{{- define "refusals/delivery/pr-actions/name-whom-ask-logins"}}name whom to ask: logins{{end}}

{{- define "refusals/delivery/pr-actions/ask-at-most-people-at-once"}}ask at most %d people at once{{end}}

{{- define "refusals/delivery/pr-actions/name-thread-its-id-prrt-or"}}name the thread: its id (PRRT_…), or a line comment in it (line-comment-…){{end}}

{{- define "refusals/delivery/pr-actions/not-draft"}}%s#%d is not a draft{{end}}

{{- define "refusals/delivery/pr-actions/dude-has-no-graphql-id-for"}}dude has no GraphQL id for %s#%d yet; try again after its next sync{{end}}

{{- define "refusals/delivery/pr-actions/no-failed-check-on-head-that"}}no failed check on %s#%d's head that GitHub can run again{{end}}

{{- define "refusals/delivery/pr-actions/failed-checks-on-were-run-again"}}the failed checks on %s#%d were run again on this head already: a fix answers them now{{end}}

{{- define "refusals/delivery/pr-actions/no-pull-request-action"}}no pull request action %q{{end}}

{{- define "refusals/delivery/pr-actions/may-not-review-not-collaborators-on"}}%s may not review %s: not collaborators on it{{end}}

{{- define "refusals/delivery/pr-actions/not-review-thread"}}%s is not a review thread of %s#%d{{end}}

{{- define "refusals/delivery/pr-actions/not-thread-in-your-feedback-you"}}%s is not a thread in your feedback: you resolve only those{{end}}

{{- define "refusals/delivery/pr-actions/github-re-run-failed-checks-on"}}GitHub's re-run of the failed checks on %s#%d may or may not have happened (%v); this head is not run again automatically{{end}}

{{- define "refusals/delivery/pr-actions/github-did-not-do"}}GitHub did not do it: %v{{end}}
