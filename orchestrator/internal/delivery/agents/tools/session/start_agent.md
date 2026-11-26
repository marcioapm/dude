Start an investigator child to research the linked repositories. The child is read-only, has no project secrets, cannot start another child, and may publish report artifacts to this session's Files. A session starts only investigators: leave profile out, or pass investigator. Use an optional prompt, note, tier, or idempotency key when needed.

{{- define "tools/session/start_agent/profile" -}}
only investigator; may be left out, and is investigator then
{{- end}}

{{- define "tools/session/start_agent/prompt" -}}
additional instructions for the child
{{- end}}

{{- define "tools/session/start_agent/note" -}}
the note passed to the child
{{- end}}

{{- define "tools/session/start_agent/tier" -}}
the model tier for the child, if one is requested
{{- end}}

{{- define "tools/session/start_agent/key" -}}
an idempotency key for this child request, if one is needed; the same key returns the same child
{{- end}}
