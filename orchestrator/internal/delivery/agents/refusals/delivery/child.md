{{define "refusals/delivery/child/profile-required"}}an existing phase profile is required{{end}}
{{define "refusals/delivery/child/parent-key-required"}}a task conductor parent and idempotency key are required{{end}}
{{define "refusals/delivery/child/capabilities-mismatch"}}capabilities must match the named profile{{end}}
{{define "refusals/delivery/child/note-too-long"}}note exceeds 4000 characters{{end}}
{{define "refusals/delivery/child/parent-unauthorized"}}the parent is not this task's authorized conductor{{end}}
{{define "refusals/delivery/child/parent-ended"}}the parent conductor is ended, replaced, or itself a child{{end}}
{{define "refusals/delivery/child/task-ended"}}this task has ended{{end}}
{{define "refusals/delivery/child/no-live-delivery"}}this task has no live delivery{{end}}
{{define "refusals/delivery/child/parent-old-attempt"}}the parent conductor is from an old delivery attempt{{end}}
{{define "refusals/delivery/child/not-independent"}}not an independent task child{{end}}
{{define "refusals/delivery/child/ownership-changed"}}child ownership changed{{end}}
{{define "refusals/delivery/child/terminal-outcome-required"}}a terminal child outcome is required{{end}}
{{define "refusals/delivery/child/wrong-task"}}child does not belong to this task{{end}}
{{define "refusals/delivery/child/restart-key-required"}}a restart idempotency key is required{{end}}
{{define "refusals/delivery/child/already-replaced"}}this child already has a replacement{{end}}
{{define "refusals/delivery/child/parent-not-live"}}the parent is no longer live for this attempt{{end}}
{{define "refusals/delivery/child/wrong-parent"}}not this task's parent{{end}}
{{define "refusals/delivery/child/parent-still-live"}}the parent has not ended{{end}}
{{define "refusals/delivery/child/task-publishing"}}a publish of this task is under way; wait for it to finish{{end}}
{{define "refusals/delivery/child/positive-attempt-required"}}a positive delivery attempt is required{{end}}
