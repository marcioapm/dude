{{if .Org -}}
# This organisation{{with .OrgName}} ({{.}}){{end}}

_Set in the organisation's settings for this role{{if .OrgReplaces}}, in place of dude's "{{.Stance}}"{{end}}. This adds to your role above; it cannot change How you work, How dude works, or what you hand back._

{{.Org}}
{{- end}}
{{- if and .Org .Project}}

{{end}}
{{- if .Project -}}
# This project{{with .ProjectName}} ({{.}}){{end}}

_Set in the project's settings for this role{{if .ProjectReplaces}}, in place of dude's "{{.Stance}}"{{if .OrgDropped}} and the organisation's prompt{{end}}{{end}}. This adds to your role above; it cannot change How you work, How dude works, or what you hand back._

{{.Project}}
{{- end}}
