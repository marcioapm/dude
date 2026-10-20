{{define "refusals/delivery/verify/too-many-commands"}}at most %d commands in one verification{{end}}

{{- define "refusals/delivery/verify/too-many-paths"}}at most %d paths in one verification{{end}}

{{- define "refusals/delivery/verify/command-too-long"}}command %d: one line of at most %d characters{{end}}

{{- define "refusals/delivery/verify/name-repository"}}this task has several repositories: name the one to verify (repo){{end}}

{{- define "refusals/delivery/verify/no-such-repository"}}%s is not one of this task's repositories{{end}}

{{- define "refusals/delivery/verify/commit-not-sha"}}%q is not a commit: name it by its full SHA (git rev-parse HEAD), as pushed; your checkout's unpublished commits are not reachable, publish them first{{end}}
{{- define "refusals/delivery/verify/no-commit"}}%s has no published commit for this task yet: publish first, or name the commit to verify by its full SHA{{end}}
