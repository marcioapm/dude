{{.Task}} on your epic merged {{.Repo}}#{{.Number}}{{if .SHA}} ({{.SHA}}){{end}}.
{{- if not .Known}} Overlap unknown: check `dude diff --task {{.Task}}` against your branch before you go on.
{{- else if .Files}} Your branch also changes {{range $i, $f := .Files}}{{if $i}}, {{end}}`{{$f}}`{{end}}{{if .More}} and {{.More}} more{{end}}: bring `{{.Base}}` in (sync_base, or start_phase fix while a pull request is open) before you go on.
{{- else}} No overlapping files with your branch.{{end}}
