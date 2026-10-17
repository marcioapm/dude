## On this epic: {{oneLine .Title}}

{{if not .Peers -}}
No other task is on this epic yet.
{{- else -}}
Your peers, the other tasks on this epic:
{{range .Peers}}
- {{.Key}} · {{oneLine .Title}}{{end}}
{{- end}}

{{if .Writer}}{{template "briefings/epic-peer-writers"}}{{else}}{{template "briefings/epic-peer-readers"}}{{end}}

### Epic notes ({{len .Notes}} of {{.NotesMax}})
{{if not .Notes}}
None pinned yet.{{end}}{{range .Notes}}
{{.N}}. {{.Text}}
   — {{.By}}{{end}}
