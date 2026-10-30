{{define "refusals/delivery/conduct-stop/you-are-no-longer-this-task"}}you are no longer this task's conductor: another took over from you. Stop nothing{{end}}

{{- define "refusals/delivery/conduct-stop/this-task-you-are-read-only"}}this task is %s: you are read-only now and stop nothing{{end}}

{{- define "refusals/delivery/conduct-stop/say-why-reason-required"}}say why you stop it: reason is required{{end}}

{{- define "refusals/delivery/conduct-stop/reason-at-most-chars"}}a reason is at most %d characters{{end}}

{{- define "refusals/delivery/conduct-stop/not-run-your-task"}}%s is not a Run of your task: you stop only child agents you started{{end}}

{{- define "refusals/delivery/conduct-stop/conductor"}}%s is a conductor: stopping yourself is the person's Stop{{end}}

{{- define "refusals/delivery/conduct-stop/phase-run"}}%s is a phase Run the delivery waits on: restart_run replaces it in its step; stopping the step is the person's Stop{{end}}

{{- define "refusals/delivery/conduct-stop/not-your-child"}}%s is not your child: you stop only child agents you started{{end}}

{{- define "refusals/delivery/conduct-stop/has-ended"}}%s has ended (%s): there is nothing to stop{{end}}
