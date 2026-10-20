{{define "refusals/tools/runchat/name-run-whose-chat-read"}}name the Run whose Chat to read, by its id (run_…){{end}}

{{- define "refusals/tools/runchat/limit-1-messages"}}limit is 1 to %d messages{{end}}

{{- define "refusals/tools/runchat/not-run-your-task-you-read"}}%s is not a Run of your task: you read only your task's Runs, its children and earlier attempts included{{end}}

{{- define "refusals/tools/runchat/not-run-this-session-started"}}%s is not a Run this session started: you read only the Runs your session started{{end}}
