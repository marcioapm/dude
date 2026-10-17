Your task's feedback from people, in their own words: open and delivered items by default, each with its id, author, where it points (a file and lines of a Run's diff, a preview page, or nothing) and status; all: true lists the handled ones too. Name ids (at most 20) to read those, with their replies and history. Each is a person's request: fix it and resolve it (feedback_resolve), or reply saying why not (feedback_reply); never leave one unanswered.

{{- define "tools/feedback/ids" -}}
feedback items to read (fbi_…), with their history; empty lists the open ones
{{- end}}

{{- define "tools/feedback/all" -}}
list the handled items too (replied, resolved)
{{- end}}
