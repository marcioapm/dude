{{define "refusals/delivery/recover/you-are-no-longer-this-task"}}you are no longer this task's conductor: another took over from you. Recover nothing{{end}}

{{- define "refusals/delivery/recover/this-task-you-are-read-only"}}this task is %s: you are read-only now and recover nothing{{end}}

{{- define "refusals/delivery/recover/note-at-most-chars"}}a note is at most %d characters{{end}}

{{- define "refusals/delivery/recover/not-run-your-task"}}%s is not a Run of your task: only this task's ended Runs are recovered{{end}}

{{- define "refusals/delivery/recover/yourself"}}%s is you: your own work is published with publish{{end}}

{{- define "refusals/delivery/recover/still-live"}}%s is still %s: stop it first (stop_run for your child, restart_run for a phase Run, or the person's Stop), then recover it once it has ended{{end}}

{{- define "refusals/delivery/recover/does-not-publish"}}%s is a %s: its work is never pushed. Only an implementer's, fixer's or simplifier's Run, or a writer child's, is recovered{{end}}

{{- define "refusals/delivery/recover/secret-found"}}a secret was found in %s's work and its push refused: a person must remove it; it is not recovered{{end}}

{{- define "refusals/delivery/recover/branch-outside"}}branch %q is outside %s: name one under it (letters, digits, . _ -, at most 60 after the prefix), or leave it out for %s{{end}}

{{- define "refusals/delivery/recover/branch-in-use"}}branch %q is the task's own or an open pull request's: name another under dude/<task>/recovered-{{end}}

{{- define "refusals/delivery/recover/already-recovered"}}%s is already recovered to %s (%s): ask for that branch to see it, or wait for its wake{{end}}
