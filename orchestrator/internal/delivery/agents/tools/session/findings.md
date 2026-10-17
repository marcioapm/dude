Review findings in a linked project: with task, that task's (name ids to read some in full); without, the project's open findings, one line each.

{{- define "tools/session/findings/project" -}}
a linked project's key (like BL); optional when only one is linked
{{- end}}

{{- define "tools/session/findings/task" -}}
a task's key (like BL-58) in that project
{{- end}}

{{- define "tools/session/findings/ids" -}}
findings: with task, those to read in full (fnd_…)
{{- end}}
