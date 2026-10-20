{{define "results/limits/policy"}}delivery policy{{end}}

{{- define "results/limits/settings"}}organisation GitHub settings{{end}}

{{- define "results/limits/dude"}}dude{{end}}

{{- define "results/limits/both"}}both{{end}}

{{- define "results/limits/delivery"}}
{{- if eq . "live"}}The task's delivery froze its delivery policy when it started: these are the bounds it is held to.
{{- else if eq . "ended"}}The task's delivery has ended: the delivery policy is what a new one would start with now, and nothing of it is spent.
{{- else}}The task has no delivery yet: the delivery policy is what one would start with now, and nothing is spent.{{end}}
{{- end}}

{{- define "results/limits/fix-rounds-note"}}A pull request fix needs both: a fix left in this review round (the delivery policy's, counted again from 0 when a person submits a new review) and one left on its pull request (the organisation's GitHub settings, counted when a fix lands with commits; 0 there is no bound). binding names the one that stops a pull request's next fix first. Past either, dude asks the owner instead of fixing.{{end}}

{{- define "results/limits/rerun-head"}}Each pull request head's failed checks are run again at most once, whoever asks: once used, CI failing again on that commit is a fix's to answer.{{end}}
