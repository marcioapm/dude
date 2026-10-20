Ask again for a linked project's repositories whose checkout failed on this session (a clone dude told you failed), and see why each failed last: dude's reason, and git's own words in `git`. Each one asked for again is cloned when your turn ends, and you are told then whether it is checked out. It never links a repository a member did not link, and it is limited to 3 calls in a Run.

{{- define "tools/session/refresh_checkout/project" -}}
{{template "tools/session/list_epics/project"}}
{{- end}}
