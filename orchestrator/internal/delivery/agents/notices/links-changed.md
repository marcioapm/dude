{{- range .Added}}A member linked {{.Key}} ({{.Name}}){{if .Repos}} with repositories: your tools now read it, and dude tells you where each repository is checked out once it is cloned.{{else}} with no repository: your tools now read its tasks, pull requests and findings, and none of its code is checked out. A member ticks its repositories in the session's Link dialog to check one out.{{end}}
{{end}}
{{- range .Removed}}A member unlinked {{.Key}} ({{.Name}}): your tools now refuse it. A checkout of it you hold stays until this session's next fresh agent; do not rely on it.
{{end}}
{{- range .Repos}}A member unlinked the repository {{.Key}}/{{.Name}}; {{.Key}} stays linked. A checkout of it you hold stays until this session's next fresh agent; do not rely on it.
{{end}}
