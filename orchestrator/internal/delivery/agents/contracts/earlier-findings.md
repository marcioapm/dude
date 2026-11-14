## Findings from the last review

{{if .ReviewOnly}}The author has pushed since your last review. For each finding, read the code as it is now and say whether it is fixed or still there; judge by the code, never by the author's reply.{{else}}A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched.{{end}} A finding with a `Done when` is judged against it: `fixed` only when it holds. Where it asks for what only a CI run, a pull request, a merge or a deploy could show, judge the part these checkouts show, or its description where they show none: `fixed` when that part holds. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.{{range $i, $f := .Findings}}

**F{{add1 $i}}** [{{$f.Severity}}] {{$f.Title}}{{if $f.File}} — `{{$f.File}}{{if gt $f.Line 0}}:{{$f.Line}}{{end}}`{{end}}{{with $f.Description}}
{{.}}{{end}}{{with $f.Acceptance}}
Done when: {{.}}{{end}}{{end}}

Answer for every one, as one more YAML document:

```yaml
verdicts:
{{range $i, $f := .Findings}}  F{{add1 $i}}: fixed | still
{{end}}```
