Mark a person's feedback item fixed by your commit (a SHA; the CLI takes --commit HEAD). It holds the pull request until your work is published and the commit is found in it; a Run that ends without publishing reopens it. With several repositories, name the repository of a general item's commit.

{{- define "tools/feedback_resolve/id" -}}
the feedback item (fbi_…)
{{- end}}

{{- define "tools/feedback_resolve/commit" -}}
the commit that fixed it: a SHA you committed in this Run
{{- end}}

{{- define "tools/feedback_resolve/repo" -}}
the repository the commit is in, when the task changes several and the item names none
{{- end}}
