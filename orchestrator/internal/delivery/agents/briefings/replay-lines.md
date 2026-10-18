{{define "briefings/replay-lines/summary"}}## Earlier, as the agent summarised it

{{.Summary}}{{with .Body}}

## Since then

{{.}}{{end}}{{end}}

{{- define "briefings/replay-lines/omitted"}}[{{.}} earlier messages omitted]{{end}}

{{- define "briefings/replay-lines/took-over"}}[A new agent took over here.]{{end}}

{{- define "briefings/replay-lines/pr-reply"}}You (on pull request {{.Repo}}#{{.Number}}): {{.Text}}{{end}}

{{- define "briefings/replay-lines/you"}}You: {{.}}{{end}}

{{- define "briefings/replay-lines/images"}}{{range .}}{{if .}} [image: {{.}}]{{else}} [image]{{end}}{{end}}{{end}}

{{- define "briefings/replay-lines/said"}}{{.Who}}: {{.Text}}{{template "briefings/replay-lines/images" .Images}}{{end}}

{{- define "briefings/replay-lines/answered"}}{{.Who}} answered: {{.Text}}{{template "briefings/replay-lines/images" .Images}}{{with .Note}}
{{$.Who}}, also: {{.}}{{end}}{{end}}

{{- define "briefings/replay-lines/image-ref"}}[image: {{.}}]{{end}}

{{- define "briefings/replay-lines/github"}}{{.}} (GitHub){{end}}

{{- define "briefings/replay-lines/someone"}}Someone{{end}}

{{- define "briefings/replay-lines/withdrawn"}}dude: your question was withdrawn; nobody answered it.{{end}}

{{- define "briefings/replay-lines/settled-elsewhere"}}dude: your question was settled elsewhere, unanswered.{{end}}

{{- define "briefings/replay-lines/dude"}}dude: {{.}}{{end}}

{{- define "briefings/replay-lines/earlier-proposal"}}Your earlier proposal: {{join . ", "}}.{{end}}

{{- define "briefings/replay-lines/filed"}}item {{.N}} filed by {{with .By}}{{.}}{{else}}someone{{end}} as {{.Key}}{{end}}

{{- define "briefings/replay-lines/proposed"}}You proposed: {{range $i, $it := .Items}}{{if $i}}; {{end}}{{add1 $i}}. {{template "briefings/replay-lines/proposed-item" $it}}{{end}} — {{with .Filed}}{{join . ", "}}{{else}}none filed{{end}}.{{end}}

{{- define "briefings/replay-lines/proposed-item"}}
{{- if or (eq .Kind "epic") (eq .Kind "task")}}{{.Kind}} {{printf "%q" .Title}} in {{.Project}}
{{- else if eq .Kind "edit"}}an edit to {{.Task}}
{{- else if eq .Kind "comment"}}a comment on {{.Task}}
{{- else if and (eq .Kind "note") .Replaces}}a note replacing note {{.Replaces}} on {{printf "%q" .Epic}} in {{.Project}}
{{- else if eq .Kind "note"}}a note on {{printf "%q" .Epic}} in {{.Project}}
{{- else}}{{.Kind}}{{end}}{{end}}

{{- define "briefings/replay-lines/decided"}}You decided, at {{.Point}}: {{.Action}}{{with .Phase}} ({{.}}){{end}}{{with .Note}}: {{oneLine .}}{{end}}.{{end}}

{{- define "briefings/replay-lines/finding"}}Finding {{.ID}}: {{.Settled}}.{{end}}

{{- define "briefings/replay-lines/task-changed"}}You changed the task's {{if .Goal}}goal{{if .Criteria}} and {{end}}{{end}}{{if .Criteria}}acceptance criteria{{end}}.{{end}}

{{- define "briefings/replay-lines/renamed"}}{{if .You}}You{{else}}{{.Who}}{{end}} named the session {{printf "%q" .Title}}.{{end}}

{{- define "briefings/replay-lines/session-replaced"}}[Here the agent's session restarted without its conversation.]{{end}}

{{- define "briefings/replay-lines/asked"}}You asked{{with .To}} {{.}}{{end}}
{{- if not .Items}}: {{.Prompt}}
{{- else if eq (len .Items) 1}}: {{template "briefings/replay-lines/asked-item" index .Items 0}}
{{- else}}:{{range $i, $it := .Items}}
{{add1 $i}}. {{$it.Header}} — {{template "briefings/replay-lines/asked-item" $it}}{{end}}{{end}}{{end}}

{{- define "briefings/replay-lines/asked-item"}}{{.Question}}{{with .Labels}} (choices: {{join . "; "}}){{end}}{{end}}

{{- define "briefings/replay-lines/tool"}}{{with .}}{{.}}{{else}}tool{{end}}{{end}}

{{- define "briefings/replay-lines/no-output-recorded"}}[no output recorded]{{end}}

{{- define "briefings/replay-lines/output-dropped"}}[output dropped]{{end}}

{{- define "briefings/replay-lines/no-output"}}[no output]{{end}}

{{- define "briefings/replay-lines/tail-cut"}}
[… {{.}} bytes cut …]
{{end}}

{{- define "briefings/replay-lines/capped"}}[… {{.}} bytes cut]{{end}}

{{- define "briefings/replay-lines/stderr"}}stderr: {{.}}{{end}}

{{- define "briefings/replay-lines/error"}}error: {{.}}{{end}}

{{- define "briefings/replay-lines/exit"}} (exit {{.}}){{end}}
