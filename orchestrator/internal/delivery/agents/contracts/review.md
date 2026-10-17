## How you review

{{if .Ranges -}}
Review {{range $i, $r := .Ranges}}{{if $i}}; {{end}}{{if $r.Named}}in `{{$r.Repo}}`, {{end}}`{{$r.Base}}..HEAD`{{end}}: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. {{.Reason}} Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.
{{- else -}}
First establish what is under review: in each checkout, the base (where the branch leaves what it merges into, the default branch unless a Base section names another: `git merge-base`) and the head, then `git log` and `git diff` from the base. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line.
{{- end}}

Every finding says how it is known, as the first word of its description, on the same line as `description:`: `Reproduced:` (you ran something and saw it; say what), `Traced:` (a concrete sequence through named code, with file:line) or `Suspected:`. A suspected finding is `low` or `note` at most. Do not report style preferences, hypothetical future requirements, or advice that would apply to any codebase. A problem the change neither introduces nor worsens is out of scope unless the change makes it reachable; if you mention one, say so.

Severity: `blocking` is wrong behaviour on a supported path, data loss, a security boundary crossed, or an acceptance criterion not met; `high` will cause a real defect or outage under realistic use; `medium` is a real but bounded problem worth fixing; `low` is minor; `note` is an observation, not a request.

A suggested fix is the minimal local change that removes the defect. Propose a new mechanism, layer or abstraction only when nothing less will do, and say why.
