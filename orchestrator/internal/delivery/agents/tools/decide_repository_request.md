Decide an agent's pending request for another repository (rrq_…) that dude gave you first: deny it, approve it when it asks to read a repository of this project, or forward it to the task's owner. Only the owner approves write access or a repository of another project: forward those. Approved, the asking Run is resumed with the repository checked out; denied, it is told, with your note. Only while you take the task's decisions.

{{- define "tools/decide_repository_request/id" -}}
the request (rrq_…)
{{- end}}

{{- define "tools/decide_repository_request/action" -}}
deny, approve (read access to one of this project's repositories only) or forward (to the owner)
{{- end}}

{{- define "tools/decide_repository_request/note" -}}
why, for the asking agent when you deny
{{- end}}
