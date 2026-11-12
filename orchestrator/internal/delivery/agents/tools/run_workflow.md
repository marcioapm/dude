Start a GitHub Actions workflow (workflow_dispatch) on one of your task's repositories, and have dude follow its run to the end. The workflow must have a workflow_dispatch trigger at that ref. One that also runs on push or pull_request is CI this change already runs: dispatch it yourself. Any other (release, deploy, schedule-only) is the person's to decide: ask them (ask_person) naming the workflow, and pass their answered question as questionId.

{{- define "tools/run_workflow/repository" -}}
the repository, by name: one of your task's; may be left out when it has one
{{- end}}

{{- define "tools/run_workflow/workflow" -}}
the workflow's file name under .github/workflows (ci.yml)
{{- end}}

{{- define "tools/run_workflow/ref" -}}
the branch or tag to run it on; default the task's branch
{{- end}}

{{- define "tools/run_workflow/inputs" -}}
the workflow's inputs, by name, as strings
{{- end}}

{{- define "tools/run_workflow/questionId" -}}
for a workflow that is not CI: the id of the person's answered question on this task that named it
{{- end}}
