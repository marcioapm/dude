{{define "refusals/tools/runcalls/no-task"}}run_calls reads a writer Run of your task: you have no task{{end}}

{{- define "refusals/tools/runcalls/commit"}}commit is a commit's SHA, 7 to 40 hexadecimal characters; leave it out for the Run's HEAD{{end}}

{{- define "refusals/tools/runcalls/filter"}}filter is at most %d characters: words of a command{{end}}

{{- define "refusals/tools/runcalls/not-writer"}}%s is not an implementer, fixer or simplifier Run of your task's current attempt{{end}}

{{- define "refusals/tools/runcalls/no-writer"}}your task's current attempt has no implementer, fixer or simplifier Run yet{{end}}

{{- define "refusals/tools/runcalls/no-such-commit"}}the Run's checkouts have no commit %s{{end}}

{{- define "refusals/tools/runcalls/unread"}}dude cannot read that Run's commit: its container is not running, and no checks ask read it there{{end}}
