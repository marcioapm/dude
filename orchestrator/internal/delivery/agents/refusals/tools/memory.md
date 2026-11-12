{{define "refusals/tools/memory/query-required"}}a query is required{{end}}

{{- define "refusals/tools/memory/types-are-memory-task-epic-and"}}types are memory, task, epic, project and doc, not %q{{end}}

{{- define "refusals/tools/memory/no-guide-section"}}no section %s in dude's guide: search it with types ["doc"] for the ids it has{{end}}

{{- define "refusals/tools/memory/no-task-or-epic-in-this-project"}}no task %q or epic %q in this project (list_tasks shows them){{end}}

{{- define "refusals/tools/memory/memory-was-archived-no-longer-held"}}memory %s was archived: it is no longer held true{{end}}

{{- define "refusals/tools/memory/scope-project-or-organization"}}scope is project or organization{{end}}

{{- define "refusals/tools/memory/pin-not-granted"}}pin is the conductor's: it keeps the project's memory. If every agent on the project must have it in front of it, don't save it; tell your conductor, which remembers and pins it (or pins your saved memory by id){{end}}

{{- define "refusals/tools/memory/remember-title-required"}}a title is required: remember saves a new memory, unless id names one to pin or unpin{{end}}

{{- define "refusals/tools/memory/pin-by-id-alone"}}with an id, remember pins or unpins that memory: give pin true or false, and nothing else{{end}}

{{- define "refusals/tools/memory/persons-pin"}}memory %s was pinned by %s: a person's pin is theirs, and only a person unpins it. Tell them, through your task, why it no longer holds{{end}}
