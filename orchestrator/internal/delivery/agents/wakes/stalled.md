Your {{.Who}} Run {{.RunID}} ({{with .Round}}round {{.}}, {{end}}{{with .Tier}}{{.}}, {{end}}running {{.Running}}) has made no progress for {{.Window}}.
{{- with .Silent}} Its agent has done nothing for {{.}}: no output, no tool call.{{end}}
{{- if .Calls}} It has {{if eq (len .Calls) 1}}1 tool call{{else}}{{len .Calls}} tool calls{{end}} open:
{{- range .Calls}} `{{.Tool}}` {{printf "%q" .Input}}, for {{.Open}}{{if .Last}};{{else}},{{end}}{{end}}
{{- range $i, $l := .CallLines}}{{if $i}};{{end}} {{if eq $l.Case "inside"}}`{{$l.Tool}}` runs inside the agent: no separate process is expected{{else if eq $l.Case "unknown"}}whether `{{$l.Tool}}`'s command still runs is unknown{{else if eq $l.Case "running"}}`{{$l.Tool}}`'s command is running{{else}}no process runs `{{$l.Tool}}`'s command{{end}}{{end}}.
{{- end}}
{{- range .Children}} Its `{{.Tool}}` call {{printf "%q" .Title}} has a child ({{.Kind}} · {{.ID}}) whose last event was {{.Ago}} ago: {{.Last}}
{{- with .Waiting}} — waiting on a permission nothing can answer: {{.Permission}} for {{join .Patterns ", "}}{{end}}.
{{- end}}
{{- if .PSError}} Processes: could not be read ({{.PSError}}).
{{- else if .Processes}} Processes: {{range $i, $p := .Processes}}{{if $i}}; {{end}}{{$p.Args}} ({{printf "%.1f" $p.CPU}} % CPU, up {{$p.Elapsed}}){{end}}.
{{- end}}
{{- if .HasCPU}} CPU over {{.Usage}}: {{printf "%.0f" .CPU}} s; network: {{.Net}}.
{{- else}} lux reported no CPU or network figures.
{{- end}}
{{- if .Activity}}
{{- with .FilesAt}} Its files last changed at {{.}}.{{end}} Its tool calls in the window: {{.Tools}}.
{{- with .Plan}} Its plan: {{join . "; "}}.{{end}}
{{- with .LastMessage}} Its last message: {{printf "%q" .}}.{{end}}
{{- end}}
{{- with .Options}} {{.}}{{end}}
