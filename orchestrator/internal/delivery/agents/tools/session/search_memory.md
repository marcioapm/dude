Search what is known in a linked project — memories, tasks, epics — and the organisation's own memories, by words and by meaning, best first.

{{- define "tools/session/search_memory/project" -}}
a linked project's key; optional when only one is linked
{{- end}}

{{- define "tools/session/search_memory/query" -}}
what you want to know, in words
{{- end}}

{{- define "tools/session/search_memory/types" -}}
only these: memory, task, epic, project (default all)
{{- end}}

{{- define "tools/session/search_memory/limit" -}}
how many results, 1 to 20 (default 8)
{{- end}}
