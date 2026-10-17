The project's epics and tasks, with their keys (like TEXT-12), status and who asked for them, and each one's relations: what it is blocked by (and whether that merged), what it blocks, what it is related to, and whether it can start — optionally only those mentioning some text, or only those that can start (startable). Use it before creating work, to find what already exists. With peers: true, your peers instead (the other tasks on your epic): each one's phase, whether its Run is live or parked, branch and head, files changed, its agent's last words, and its messages waiting for you.

Archived tasks are left out unless archived: true; each is marked archived. Each task's resolution says why a person ended it: wont_do, duplicate, cant_reproduce, or nothing_to_merge (done, nothing merged); empty while it hasn't.

{{- define "tools/list_tasks/text" -}}
only tasks whose title or goal mention this (case-insensitive); empty lists everything
{{- end}}

{{- define "tools/list_tasks/peers" -}}
your peers instead: the other tasks on your epic, each with its phase, live or parked, branch and head, files changed, its agent's last words and the messages it has waiting for you; your own task is marked self
{{- end}}

{{- define "tools/list_tasks/startable" -}}
only tasks not started with nothing unmerged in front of them
{{- end}}

{{- define "tools/list_tasks/archived" -}}
include archived tasks, which are left out otherwise; each is marked archived
{{- end}}

{{- define "tools/list_tasks/closed" -}}
only tasks a person closed (won't do, duplicate, can't reproduce)
{{- end}}
