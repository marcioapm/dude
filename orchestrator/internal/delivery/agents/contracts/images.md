{{define "contracts/images/given"}}[Image {{.N}}: {{.Name}}]{{end}}

{{- define "contracts/images/unavailable"}}[Image unavailable: {{.}}]{{end}}

{{- define "contracts/images/named"}}[Image: {{.}}]{{end}}

{{- define "contracts/images/from-session"}}Images shared in the brainstorm this task came from, showing what to build:

{{join . "\n"}}{{end}}

{{- define "contracts/images/shared-ids"}}[Images with this message, by id for a task item's images: {{range $i, $a := .}}{{if $i}}, {{end}}{{$a.Name}} ({{$a.ID}}){{end}}]{{end}}
