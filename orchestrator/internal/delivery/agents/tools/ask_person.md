{{template "tools/common/ask-person"}} After calling it, end your turn: the answers are your next message.

{{- define "tools/ask_person/question" -}}
one question, with enough context to answer it; or use questions instead
{{- end}}

{{- define "tools/ask_person/choices" -}}
with question: answers to offer, when there are some
{{- end}}

{{- define "tools/ask_person/questions" -}}
1 to 4 questions asked together, each with a header and its own choices; the person answers them all at once
{{- end}}

{{- define "tools/ask_person/questions.header" -}}
a few words naming it, shown as its tab (at most 24 characters), like Retry scope
{{- end}}

{{- define "tools/ask_person/questions.question" -}}
the question, with enough context to answer it (at most 4000 characters)
{{- end}}

{{- define "tools/ask_person/questions.choices" -}}
0 to 6 answers to offer, when there are sensible options; never an other or something-else choice: dude adds one
{{- end}}

{{- define "tools/ask_person/questions.choices.label" -}}
the answer, short (at most 120 characters); left out for a choice with a note, whose label dude writes as its action and note
{{- end}}

{{- define "tools/ask_person/questions.choices.note" -}}
a conductor's, on its question about an escalation, for a choice whose action needs the person's reason or direction (a waiver, an acceptance, a stuck or non-converging retry, an amend): the reason or direction you propose, in plain words. The person sees "Waive: <note>"; the owner picking it decides the escalation with your note as theirs. Required for such an action, refused on any other
{{- end}}

{{- define "tools/ask_person/questions.choices.description" -}}
one line on what it means or why (at most 300 characters)
{{- end}}

{{- define "tools/ask_person/questions.choices.recommended" -}}
the one you would pick, shown as your suggestion; at most one per question
{{- end}}

{{- define "tools/ask_person/questions.multiple" -}}
the person may pick several choices (default one)
{{- end}}

{{- define "tools/ask_person/actions" -}}
a conductor's, while an escalation waits on a person, with question and choices: the escalation action each choice stands for, one per choice, in order (retry, accept, resume, done, wait, stop); the owner picking a choice decides the escalation with it. A choice whose action needs the person's reason or direction (waive, accept of a failed test, a stuck retry, a non-converging loop's retry, accept or amend) carries the one you propose as its note, in questions' choices
{{- end}}
