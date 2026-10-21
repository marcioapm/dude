dude's settings as they apply here, each value with where it is set: dude (its built-in default), organization or project. Sections: delivery (the delivery policy: reviewers, including the path globs that add one, blocking severities, review and fix rounds, simplify, test, park, idle and warm minutes, the conductor's publish size), roles (each agent role's model tier, harness, time limit, machine size, image, and whether notes are set), prompts (built-in or saved, added to or replaced, saved when and by whom; never the text), github, network (egress, and which roles may search and read the web), capabilities (each role's tools) and machines. With a task, also what its delivery is held to: the frozen policy where it differs from today's, raises people granted, and the reviewers its changed paths pick. Read-only: a person changes a setting in Settings.

{{- define "tools/settings/project" -}}
a project's id, key or name; default your task's project, or for a session each linked project
{{- end}}

{{- define "tools/settings/task" -}}
a task's key or id: adds what its delivery is held to; a conductor's default is its own task
{{- end}}

{{- define "tools/settings/section" -}}
one of delivery, roles, prompts, github, network, capabilities, machines; default all
{{- end}}
