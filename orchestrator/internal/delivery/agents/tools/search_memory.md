Search what is known here: memories people and agents saved (facts, procedures, notes), and this project's tasks, epics and the project itself — by words and by meaning, best first. Search before you investigate something that may already be known, and before you remember something.

{{- define "tools/search_memory/query" -}}
what you want to know, in words: a question or the terms you would expect
{{- end}}

{{- define "tools/search_memory/types" -}}
only these: memory, task, epic, project (default all)
{{- end}}

{{- define "tools/search_memory/limit" -}}
how many results, 1 to 20 (default 8)
{{- end}}
