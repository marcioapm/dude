Answer a person's feedback item without changing the code: say why it is wrong, out of scope, or already so. It is then replied, which holds nothing back; the person may reopen it.

{{- define "tools/feedback_reply/id" -}}
the feedback item (fbi_…)
{{- end}}

{{- define "tools/feedback_reply/text" -}}
why you are not changing the code for it, to the person who wrote it
{{- end}}
