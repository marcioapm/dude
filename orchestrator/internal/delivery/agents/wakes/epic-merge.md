{{.Task}} on your epic merged {{.Repo}}#{{.Number}}{{if .SHA}} ({{.SHA}}){{end}}.
{{- if not .Known}} Overlap unknown: check `dude diff --task {{.Task}}` against your branch before you go on.
{{- else if .Shared}} Your branch also changes {{if .Files}}{{range $i, $f := .Files}}{{if $i}}, {{end}}`{{$f}}`{{end}}{{if .More}} and {{.More}} more{{end}}{{else}}{{.Shared}} file{{if ne .Shared 1}}s{{end}} it changed{{end}}
{{- if .SameBase}}: bring `{{.Base}}` in (sync_base, or start_phase fix while a pull request is open) before you go on.
{{- else}}. It merged into `{{.Base}}`, not your base: check whether your work needs it before you go on.{{end}}
{{- else}} No overlapping files with your branch.{{end}}
