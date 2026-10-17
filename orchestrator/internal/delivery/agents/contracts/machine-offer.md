{{if not .Targets -}}
Move to a larger machine. {{template "contracts/machine-nothing" .}}
{{- else -}}
Move to a larger machine, giving the size by name and a reason naming the command, its exit or error, and the workers or threads it used. You run on {{.Current.Name}} ({{.Current.Spec}}). Larger sizes you may move to: {{.List}}. A successful request ends your turn: you are resumed on the new size. {{template "contracts/machine-kept"}}
{{- end}}
