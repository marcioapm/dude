Search one of your projects' GitHub repositories for issues or pull requests (kind), by words and GitHub qualifiers, open, closed or all: up to 30, most recently updated first, each with its number, title, author, state, labels, last update and URL. Read only; dude reads it, and what people wrote comes marked as external text.

{{- define "tools/github_search/repo" -}}
the repository, owner/name, one of your projects'
{{- end}}

{{- define "tools/github_search/query" -}}
words to find in titles and bodies, and GitHub qualifiers (label:bug, author:ana); the repository is repo's, never one named here
{{- end}}

{{- define "tools/github_search/kind" -}}
issue or pr
{{- end}}

{{- define "tools/github_search/state" -}}
open, closed or all (default all)
{{- end}}
