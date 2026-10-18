Start one independent child agent for this task. The child reports its findings to you; publishing profiles may publish code. Refused for child agents.

{{- define "tools/start_agent/profile" -}}
an existing phase profile, not conductor or brainstorm
{{- end}}

{{- define "tools/start_agent/prompt" -}}
additional instructions for the child
{{- end}}

{{- define "tools/start_agent/note" -}}
the note passed to the child
{{- end}}

{{- define "tools/start_agent/tier" -}}
the model tier for the child, if one is requested
{{- end}}

{{- define "tools/start_agent/key" -}}
an idempotency key for this child request, if one is needed
{{- end}}
