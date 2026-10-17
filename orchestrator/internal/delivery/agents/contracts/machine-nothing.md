{{if .Largest -}}
You run on {{.Current.Name}} ({{.Current.Spec}}), {{with .Org}}{{.}}'s{{else}}the organization's{{end}} largest size: there is none larger to ask for. Use fewer workers or less data, and say in your work what did not fit.
{{- else -}}
You run on {{.Current.Name}} ({{.Current.Spec}}), and no larger size can take this agent: their pools lack its platform, nested containers or a host they fit. Use fewer workers or less data, and say in your work what did not fit.
{{- end}}
