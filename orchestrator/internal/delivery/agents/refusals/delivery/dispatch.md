{{define "refusals/delivery/dispatch/workflow-file"}}workflow %q is a workflow's file name under .github/workflows (ci.yml, release.yaml){{end}}

{{- define "refusals/delivery/dispatch/not-task-repository"}}%q is not one of this task's repositories: name one of %s{{end}}

{{- define "refusals/delivery/dispatch/not-on-github"}}%s is not on a GitHub dude is connected to{{end}}

{{- define "refusals/delivery/dispatch/name-ref"}}this task has no branch yet: name the ref to run the workflow on{{end}}

{{- define "refusals/delivery/dispatch/ref-unreadable"}}%q is not a branch or tag name{{end}}

{{- define "refusals/delivery/dispatch/too-many-inputs"}}at most %d inputs to a workflow{{end}}

{{- define "refusals/delivery/dispatch/input-too-long"}}input %q: a name of up to 100 characters and a value of up to %d{{end}}

{{- define "refusals/delivery/dispatch/question-not-answered"}}%s is not a question of this task a person has answered: ask the person first (ask_person) with the choices %q and %q, and pass their answered question's id as questionId{{end}}

{{- define "refusals/delivery/dispatch/question-not-naming"}}%s does not offer the choice %q: the person's answer authorizes only the workflow and ref its choice names. Ask about this one (ask_person) with the choices %q and %q{{end}}

{{- define "refusals/delivery/dispatch/question-not-approved"}}%s was answered %q, not %q: the person did not approve this dispatch. Nothing was dispatched{{end}}

{{- define "refusals/delivery/dispatch/question-spent"}}%s already authorized dispatch %s: one answer authorizes one dispatch. Ask the person again (ask_person){{end}}

{{- define "refusals/delivery/dispatch/unbound"}}%s on %s was dispatched already (%s) and dude has not yet found its run: GitHub answered that dispatch with no run id, so a second one now would leave dude unable to tell their runs apart. Wait for it to be bound or unknown (wait_for workflowEnded, or pull_requests), then dispatch again{{end}}

{{- define "refusals/delivery/dispatch/no-such-workflow"}}there is no .github/workflows/%s at %s in %s{{end}}

{{- define "refusals/delivery/dispatch/triggers-unreadable"}}dude cannot read the triggers (on:) of %s at %s: %v. Nothing was dispatched{{end}}

{{- define "refusals/delivery/dispatch/no-trigger"}}%s at %s has no workflow_dispatch trigger (it runs on %s): GitHub cannot start it by hand. Nothing was dispatched{{end}}

{{- define "refusals/delivery/dispatch/needs-person"}}%s runs on %s, but %s: it is not CI this change already runs, and the person decides it. Ask them (ask_person) with the choices %q and %q; once they have picked %q, call run_workflow again with questionId{{end}}
