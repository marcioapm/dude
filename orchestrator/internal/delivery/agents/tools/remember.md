Save something worth knowing next time, for every agent and person on this project: a fact that holds ("the billing API paginates by cursor"), a procedure that works, a trap and its way around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save what the code or the task already says.

{{- define "tools/remember/title" -}}
one line that says what it is, as another agent would scan it in a list
{{- end}}

{{- define "tools/remember/content" -}}
the fact, procedure or note, in Markdown: what, and why it holds
{{- end}}

{{- define "tools/remember/kind" -}}
fact (default), procedure (steps that work) or note
{{- end}}

{{- define "tools/remember/about" -}}
task keys (TEXT-12) or epic titles this is about, in this project
{{- end}}

{{- define "tools/remember/scope" -}}
project (default): this project only; organization: true in every project
{{- end}}
