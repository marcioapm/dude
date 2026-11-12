Save something worth knowing next time, for every agent and person on this project: a fact that holds ("the billing API paginates by cursor"), a procedure that works, a trap and its way around. It is live at once, and marked as yours. Search first so you do not save it twice; do not save what the code or the task already says.

{{- define "tools/remember/id" -}}
a conductor's: an existing project fact's id, with pin true or false and nothing else, to pin or unpin it instead of saving one: pin what a phase Run saved, or unpin a pinned fact that has gone stale. A person's pin is theirs: only a person unpins it
{{- end}}

{{- define "tools/remember/title" -}}
one line that says what it is, as another agent would scan it in a list; required unless id is given
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

{{- define "tools/remember/pin" -}}
a conductor's: true for a fact every future agent on this project must have in front of it: pinned facts arrive in every briefing. With id, true pins that fact and false unpins it
{{- end}}
