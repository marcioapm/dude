Record a piece of work you found that is outside your task — a bug, a follow-up, a part to split out — as a new task in this project. It is not started: a person reads it and decides. The goal is required (at least {{.GoalMin}} characters): say why it matters and what should change. A conductor or investigator may relate it (blockedBy, blocks, related: task keys); a refused relation refuses the whole create.

{{- define "tools/create_task/title" -}}
what should change, in one line
{{- end}}

{{- define "tools/create_task/goal" -}}
required: why it matters, what should change, and any detail another agent or a person needs; see the tool's description for the minimum
{{- end}}

{{- define "tools/create_task/acceptanceCriteria" -}}
things that must be true when it is done
{{- end}}

{{- define "tools/create_task/epic" -}}
an existing epic's title to put it in (see list_tasks); none leaves it outside any
{{- end}}

{{- define "tools/create_task/blockedBy" -}}
conductor, investigator: task keys (CP-41) the new task can't start before they are merged
{{- end}}

{{- define "tools/create_task/blocks" -}}
conductor, investigator: task keys that can't start before the new task is merged
{{- end}}

{{- define "tools/create_task/related" -}}
conductor, investigator: task keys related to it, gating nothing
{{- end}}
