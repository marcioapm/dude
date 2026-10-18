What a Run of your task changed: this Run's checkout, uncommitted work included, against the commit it started from — one snapshot, no history. Without paths, the changed files, most changed first, with line counts and no lines (paged by limit and offset; hasMore says there is another page). With paths, those files' changes as unified diff text, at most 2,000 lines in all. With task (a peer's key), that peer's code as dude last stored it, the same way.

{{- define "tools/run_diff/run" -}}
a Run of this task (run_…); empty is your own
{{- end}}

{{- define "tools/run_diff/task" -}}
a peer's key (like ABS-2) instead of a Run: its current Run's stored diff, or its latest if none is going
{{- end}}

{{- define "tools/run_diff/paths" -}}
files to show the changes of, as the list names them (exact); empty lists the files
{{- end}}

{{- define "tools/run_diff/nameStatus" -}}
list only each file's path and status (M, A, D, R): the cheapest form
{{- end}}

{{- define "tools/run_diff/limit" -}}
files per page of the list, 1 to 1000 (default 200)
{{- end}}

{{- define "tools/run_diff/offset" -}}
files of the list to skip, for the next page
{{- end}}
