Ready for your look:
{{- range .Servers}}
- {{.Name}}{{with .URL}} — {{.}}{{end}}
{{- end}}

{{if .Task}}Check this task's change there, on its branch as it is now.{{else}}Check `{{.Branch}}` there, as the branch is now.{{end}}
