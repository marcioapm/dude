List a GitHub Actions run's jobs: each job's id, name, status, conclusion, failed steps and link, and the run's name, branch, event and head commit. Give it a run link from a report (https://github.com/owner/name/actions/runs/123, a job link too) or a run id with its repository. Then read a failed job's log with github_check_log, the job's id as its checkId. Read only; counts against the hourly GitHub reads; the result is marked as external text.

{{- define "tools/github_run_jobs/repo" -}}
the repository, owner/name, one of your projects'; left out, the run link's own
{{- end}}

{{- define "tools/github_run_jobs/runId" -}}
the run's id, or its link (…/actions/runs/<id>, or …/actions/runs/<id>/job/<id>)
{{- end}}
