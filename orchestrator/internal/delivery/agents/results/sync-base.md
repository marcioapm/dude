{{define "results/sync-base/merge"}}`git merge-tree --write-tree HEAD lux/{{.}}` shows whether it conflicts; then `git merge lux/{{.}}`, resolve, run the affected tests, commit{{end -}}
{{if eq .Case "asked-recorded" -}}
lux has not reported the fetch yet, so lux/{{.Base}} may be missing or older than the base you integrate: call sync_base again in a moment. {{.MergeWhen}}. Then {{template "results/sync-base/merge" .Base}}.
{{- else if eq .Case "asked" -}}
lux has not reported the fetch yet: in a moment, `git rev-list --count HEAD..lux/{{.Base}}` says how far behind you are (an error: not fetched yet); then {{template "results/sync-base/merge" .Base}}.
{{- else if eq .Case "earlier" -}}
lux has not reported this call's fetch; its last fetch, for an earlier call, landed {{.To}} on lux/{{.Base}}: `git rev-list --count HEAD..lux/{{.Base}}` says how far behind you are; then {{template "results/sync-base/merge" .Base}}.
{{- else if eq .Case "failed" -}}
lux could not fetch it; call sync_base again, or carry on without it.
{{- else if eq .Case "up-to-date" -}}
You already have everything on {{.Base}}: nothing to merge.
{{- else -}}
{{.Base}} is {{.Behind}} commits ahead of you: {{template "results/sync-base/merge" .Base}}.
{{- end}}
