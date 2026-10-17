Run the failed checks on one of your task's pull requests again, on GitHub: once per head, for a failure that is not the code's (a flaky runner, an outage). A real failure is a fix's.

{{- define "tools/rerun_failed_checks/pr" -}}
{{template "tools/mark_ready/pr"}}
{{- end}}
