Write what Chat settled into the task: its goal, its acceptance criteria, or both (criteria replace the list). Do it before you start the implementer, whenever you and the person agreed something the task's text does not say, so the implementer's prompt has it. Refused once an implementer has started on this attempt. Its relations (addRelations, removeRelations: blocked_by, blocks, related, by task key) may change at any status, when you learn this needs other work merged first; you remove only relations this task's agents added.

{{- define "tools/update_task/goal" -}}
the task's goal, whole, as agreed
{{- end}}

{{- define "tools/update_task/acceptanceCriteria" -}}
the task's acceptance criteria, the whole list, as agreed
{{- end}}

{{- define "tools/update_task/addRelations" -}}
relations to add to this task, at any status: blocked_by (it can't start before that task is merged), blocks, related. One already there as the same kind is left as it is and named in the answer; a different relation between the same two tasks is refused
{{- end}}

{{- define "tools/update_task/addRelations.kind" -}}
blocked_by, blocks or related, read from this task
{{- end}}

{{- define "tools/update_task/addRelations.task" -}}
the other task's key (CP-41)
{{- end}}

{{- define "tools/update_task/addRelations.why" -}}
why, shown quoted in the task's history
{{- end}}

{{- define "tools/update_task/removeRelations" -}}
relations to remove: only ones this task's agents added
{{- end}}

{{- define "tools/update_task/removeRelations.kind" -}}
{{template "tools/update_task/addRelations.kind"}}
{{- end}}

{{- define "tools/update_task/removeRelations.task" -}}
{{template "tools/update_task/addRelations.task"}}
{{- end}}

{{- define "tools/update_task/removeRelations.why" -}}
{{template "tools/update_task/addRelations.why"}}
{{- end}}
