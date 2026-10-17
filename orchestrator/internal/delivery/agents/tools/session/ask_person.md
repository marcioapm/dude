{{template "tools/common/ask-person"}} Then end your turn: the answers are your next message. With to (a member's name), only that member can answer; what others say meanwhile reaches you with the answer.

{{- define "tools/session/ask_person/question" -}}
one question, with enough context to answer it; or use questions instead
{{- end}}

{{- define "tools/session/ask_person/choices" -}}
with question: answers to offer
{{- end}}

{{- define "tools/session/ask_person/questions" -}}
1 to 4 questions asked together, each with a header and its own choices; answered all at once
{{- end}}

{{- define "tools/session/ask_person/questions.header" -}}
{{template "tools/ask_person/questions.header"}}
{{- end}}

{{- define "tools/session/ask_person/questions.question" -}}
{{template "tools/ask_person/questions.question"}}
{{- end}}

{{- define "tools/session/ask_person/questions.choices" -}}
{{template "tools/ask_person/questions.choices"}}
{{- end}}

{{- define "tools/session/ask_person/questions.choices.label" -}}
{{template "tools/ask_person/questions.choices.label"}}
{{- end}}

{{- define "tools/session/ask_person/questions.choices.description" -}}
{{template "tools/ask_person/questions.choices.description"}}
{{- end}}

{{- define "tools/session/ask_person/questions.choices.recommended" -}}
{{template "tools/ask_person/questions.choices.recommended"}}
{{- end}}

{{- define "tools/session/ask_person/questions.multiple" -}}
{{template "tools/ask_person/questions.multiple"}}
{{- end}}

{{- define "tools/session/ask_person/to" -}}
a member's name: only they answer; others' messages meanwhile come with the answer
{{- end}}
