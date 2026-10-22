Search what is known here: memories people and agents saved (facts, procedures, notes), this project's tasks, epics and the project itself, and dude's own guide (type doc: how dude works and why, written by dude, not by people here) — by words and by meaning, best first. Search before you investigate something that may already be known, and before you remember something. A doc result has a link to its page in dude's web app (a path starting #/ when this deployment has no public address): give it to the person you answer.

{{- define "tools/search_memory/query" -}}
what you want to know, in words: a question or the terms you would expect
{{- end}}

{{- define "tools/search_memory/types" -}}
only these: memory, task, epic, project, doc (default all)
{{- end}}

{{- define "tools/search_memory/limit" -}}
how many results, 1 to 20 (default 8)
{{- end}}
