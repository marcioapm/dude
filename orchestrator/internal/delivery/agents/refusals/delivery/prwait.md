{{define "refusals/delivery/prwait/delivery-moved-on-as-you-asked"}}the delivery moved on as you asked; you are woken at its next decision{{end}}

{{- define "refusals/delivery/prwait/cannot-start-while-delivery-waits-on"}}%s cannot start while the delivery waits on its pull requests; it can start %s{{end}}

{{- define "refusals/delivery/prwait/delivery-reading-what-changed-on-its"}}the delivery is reading what changed on its pull requests now; start %s again in a moment{{end}}

{{- define "refusals/delivery/prwait/run-or-publish-changing-task-branch"}}a Run or a publish is changing the task branch now: one writer at a time; start %s once it is done{{end}}

{{- define "refusals/delivery/prwait/fix-while-pull-request-waits-addresses"}}a fix while the pull request waits addresses what you ask in note, not findings: review first{{end}}

{{- define "refusals/delivery/prwait/say-what-fix-do-note-no"}}say what the fix is to do (note): no pull request feedback is waiting for it{{end}}
