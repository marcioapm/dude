{{define "results/machines/list"}}The larger machine sizes this Run may move to, with their pools: {{.}}.{{end}}

{{- define "results/machines/list-none"}}The larger machine sizes this Run may move to: none. {{.}}{{end}}

{{- define "results/machines/not-larger"}}you run on {{.Name}} ({{.Spec}}); ask for one larger: {{.Names}}{{end}}

{{- define "results/machines/not-live"}}this Run is not live and available for a machine move{{end}}

{{- define "results/machines/your-machine"}}your machine{{end}}

{{- define "results/machines/another-size"}}another size{{end}}
