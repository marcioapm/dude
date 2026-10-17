## Your machine

{{if not .C.Targets -}}
{{template "contracts/machine-nothing" .C}}
{{- else -}}
You run on {{.C.Current.Name}} ({{.C.Current.Spec}}){{with .Raised}}, raised for this {{.Scope}} after {{.Who}} asked: {{printf "%q" .Reason}}{{end}}. Larger sizes you may move to: {{.C.List}}. If something you need to run is killed for memory (exit 137, "Killed", an out-of-memory error) or is far too slow for its CPUs, do not call it flaky: run it again with fewer workers if that is enough, and otherwise {{if .CLI}}move with `dude machine request SIZE --reason "…"`{{else}}call request_machine with the size and your reason{{end}}, saying what ran out. {{template "contracts/machine-kept"}}
{{- end}}
