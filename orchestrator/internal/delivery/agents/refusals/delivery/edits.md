{{define "refusals/delivery/edits/you-are-no-longer-this-task-3"}}you are no longer this task's conductor: another took over from you. Publish nothing{{end}}

{{- define "refusals/delivery/edits/this-task-you-are-read-only-3"}}this task is %s: you are read-only now and publish nothing. Offer a follow-up task (create_task){{end}}

{{- define "refusals/delivery/edits/this-task-has-no-delivery-in-2"}}this task has no delivery in progress: there is no task branch to publish to{{end}}

{{- define "refusals/delivery/edits/your-checkout-read-only-for-this"}}your checkout is read-only for this conversation: %s{{end}}

{{- define "refusals/delivery/edits/your-checkout-has-no-repository-you"}}your checkout has no repository you may change: nothing can be published{{end}}

{{- define "refusals/delivery/edits/publish-yours-under-way-you-are"}}a publish of yours is under way: you are woken when it is done{{end}}

{{- define "refusals/delivery/edits/pull-request-push-unreviewed"}}the pull request in %s shows %s, which dude pushed and no review has covered yet: nothing more goes to it until one has. Use start_phase review first{{end}}

{{- define "refusals/delivery/edits/pull-request-open-on-branch"}}a pull request is open on this branch, so changes reach it only after review on a candidate: use start_phase fix with a note saying what to change{{end}}
