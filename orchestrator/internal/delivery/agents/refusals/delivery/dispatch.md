{{define "refusals/delivery/dispatch/workflow-file"}}workflow %q is a workflow's file name under .github/workflows (ci.yml, release.yaml){{end}}

{{- define "refusals/delivery/dispatch/not-task-repository"}}%q is not one of this task's repositories: name one of %s{{end}}

{{- define "refusals/delivery/dispatch/not-on-github"}}%s is not on a GitHub dude is connected to{{end}}

{{- define "refusals/delivery/dispatch/name-ref"}}this task has no branch yet: name the ref to run the workflow on{{end}}

{{- define "refusals/delivery/dispatch/ref-unreadable"}}%q is not a branch or tag name{{end}}

{{- define "refusals/delivery/dispatch/too-many-inputs"}}at most %d inputs to a workflow{{end}}

{{- define "refusals/delivery/dispatch/input-too-long"}}input %q: a name of up to 100 characters and a value of up to %d{{end}}

{{- define "refusals/delivery/dispatch/question-not-answered"}}%s is not a question of this task a person has answered: ask the person first (ask_person), naming the workflow, and pass their answered question's id as questionId{{end}}

{{- define "refusals/delivery/dispatch/question-not-naming"}}%s does not name %s: the person's answer authorizes the workflow their question named. Ask about this one (ask_person){{end}}

{{- define "refusals/delivery/dispatch/no-such-workflow"}}there is no .github/workflows/%s at %s in %s{{end}}

{{- define "refusals/delivery/dispatch/triggers-unreadable"}}dude cannot read the triggers (on:) of %s at %s: %v. Nothing was dispatched{{end}}

{{- define "refusals/delivery/dispatch/no-trigger"}}%s at %s has no workflow_dispatch trigger (it runs on %s): GitHub cannot start it by hand. Nothing was dispatched{{end}}

{{- define "refusals/delivery/dispatch/needs-person"}}%s runs on %s, not on a push or pull request: it is not CI this change already runs, and the person decides it. Ask them (ask_person), naming %s and the ref %s; once they have answered yes, call run_workflow again with questionId{{end}}
