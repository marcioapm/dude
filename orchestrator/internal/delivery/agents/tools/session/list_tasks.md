A linked project's epics and tasks, with their keys (like BL-12), status and who asked for them — optionally only those mentioning some text.

{{- define "tools/session/list_tasks/project" -}}
a linked project's key (like BL); optional when only one is linked
{{- end}}

{{- define "tools/session/list_tasks/text" -}}
only tasks whose title or goal mention this
{{- end}}
