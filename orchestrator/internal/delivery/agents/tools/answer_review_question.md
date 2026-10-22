Answer a reviewer's open question on this task (rvq_…) with a fact you established from the code, its configuration or its history, or withdraw it as moot with the reason. The next reviewer of its category is shown your answer as the conductor's, never as a person's decision; what only a person can say, ask them (ask_person) and let them answer it on the task. Only while you take the task's decisions.

{{- define "tools/answer_review_question/id" -}}
the question (rvq_…)
{{- end}}

{{- define "tools/answer_review_question/answer" -}}
the fact, and where you established it; leave out to withdraw
{{- end}}

{{- define "tools/answer_review_question/withdraw" -}}
true to withdraw the question as moot instead of answering it
{{- end}}

{{- define "tools/answer_review_question/reason" -}}
why it no longer matters, with withdraw
{{- end}}
