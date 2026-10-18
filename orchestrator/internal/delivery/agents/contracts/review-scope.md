{{if .Ranges -}}
Review {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`{{$r.Base}}..HEAD`{{end}}: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. {{.Reason}} Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.
{{- else -}}
First establish what is under review: in each checkout, the base (where the branch leaves what it merges into, the default branch unless a Base section names another: `git merge-base`) and the head, then `git log` and `git diff` from the base. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line.
{{- end}}
