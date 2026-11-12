Start a GitHub Actions workflow (workflow_dispatch) on one of your task's repositories, and have dude follow its run to the end, re-runs included. The workflow must have a workflow_dispatch trigger at that ref. One that already runs for this task's own change (on a push to its branch, or a pull request into its base, as its filters say) is its CI: dispatch it yourself on the task's branch. Any other (a deploy on push to main, a release on tags, a schedule, filters dude cannot read, or another ref) is the person's to decide: run_workflow's refusal names the two choices to ask them with (ask_person); pass their answered question as questionId. One answer authorizes one dispatch.

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
for a workflow that is not this task's CI: the id of the person's question on this task answered with the approving choice the refusal named
{{- end}}
