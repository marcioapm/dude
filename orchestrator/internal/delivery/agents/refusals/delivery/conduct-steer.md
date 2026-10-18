{{define "refusals/delivery/conduct-steer/you-are-no-longer-this-task-2"}}you are no longer this task's conductor: another took over from you. Steer nothing{{end}}

{{- define "refusals/delivery/conduct-steer/this-task-you-are-read-only-2"}}this task is %s: you are read-only now and steer nothing. Offer to record the change as a follow-up task (create_task), linked to this one, and create it once the person agrees{{end}}

{{- define "refusals/delivery/conduct-steer/say-what-tell-run-text-required"}}say what to tell the Run: text is required{{end}}

{{- define "refusals/delivery/conduct-steer/steer-at-most-kib-text"}}a steer is at most %d KiB of text{{end}}

{{- define "refusals/delivery/conduct-steer/not-run-your-task-you-steer"}}%s is not a Run of your task: you steer only the Runs of this task{{end}}

{{- define "refusals/delivery/conduct-steer/conductor-not-phase-run-steer-runs"}}%s is a conductor, not a phase Run: steer the Runs you started{{end}}

{{- define "refusals/delivery/conduct-steer/branch-preview-not-agent-there-nobody"}}%s is a branch preview, not an agent: there is nobody to steer{{end}}

{{- define "refusals/delivery/conduct-steer/not-phase-run"}}%s is not a phase Run{{end}}

{{- define "refusals/delivery/conduct-steer/run-earlier-attempt-steer-only-current"}}%s is a Run of an earlier attempt: steer only the current attempt's{{end}}

{{- define "refusals/delivery/conduct-steer/ending-would-never-read-steer-wait"}}%s is ending: it would never read a steer. Wait for it to end, then start another phase{{end}}

{{- define "refusals/delivery/conduct-steer/has-ended-start-another-phase-instead"}}%s has ended (%s): start another phase instead of steering it{{end}}

{{- define "refusals/delivery/conduct-steer/run-feedback-goes-implementer-or-fixer"}}%s is a %s Run: feedback goes to an implementer or fixer, which can change the code{{end}}

{{- define "refusals/delivery/conduct-steer/not-open-feedback-item-this-task"}}%s is not an open feedback item of this task's current attempt (feedback lists them){{end}}
