Move to a larger machine.

{{- define "tools/request_machine/size" -}}
one of the sizes listed in this tool's description, by name (Large)
{{- end}}

{{- define "tools/request_machine/reason" -}}
what ran out: the command, its exit or error, the workers or threads it used
{{- end}}
