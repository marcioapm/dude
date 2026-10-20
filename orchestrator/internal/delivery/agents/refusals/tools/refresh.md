{{define "refusals/tools/refresh/no-open-pull-request-refresh"}}the task has no open pull request to refresh: pull_requests lists the ones it had{{end}}

{{- define "refusals/tools/refresh/no-open-pull-request-on-github"}}none of the task's open pull requests is on GitHub: pull_requests lists them as dude has them{{end}}

{{- define "refusals/tools/refresh/name-task-whose-pull-requests-refresh"}}name the task whose pull requests to refresh, by its key{{end}}

{{- define "refusals/tools/refresh/refreshing-takes-github-reads"}}a refresh takes one GitHub read for each open pull request, %d here, and %d of the hour's %d are left: wait, or work with what pull_requests last read{{end}}
