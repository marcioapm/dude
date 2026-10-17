End your turn until the reply to one of your messages comes. Use it only when the next thing you must do depends on that answer and nothing else is left that doesn't. Your Run parks if no reply comes within the project's grace (10 minutes by default); the reply resumes it. If the reply has already come, it is returned at once and you carry on.

{{- define "tools/wait_for_reply/message" -}}
one of your own messages (pm_…) whose reply you cannot go on without
{{- end}}
