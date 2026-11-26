List a branch's recent GitHub Actions runs, newest first: each run's workflow, event, head commit, time and conclusion, and each of its jobs' id, name and conclusion. Left out, the branch is the repository's default branch, so you can see whether a job failing on your pull request also fails, or fails only sometimes, on the base. Only the newest runs are listed, which on a busy branch are pushes and pull requests: to find a scheduled (nightly) run, pass event: schedule and/or its workflow file (workflow: nightly.yml). Read a failed job's log with github_check_log, the job's id as its checkId. Read only; counts against the hourly GitHub reads; the result is marked as external text.

{{- define "tools/github_branch_runs/repo" -}}
the repository, owner/name, one of your projects'; may be left out when there is only one
{{- end}}

{{- define "tools/github_branch_runs/branch" -}}
the branch whose runs to list; left out, the repository's default branch
{{- end}}

{{- define "tools/github_branch_runs/workflow" -}}
only this workflow's runs: its file name under .github/workflows (like nightly_warehouse_snowflake.yml) or its numeric id; may be left out
{{- end}}

{{- define "tools/github_branch_runs/event" -}}
only runs this event triggered, like schedule (a nightly), push, pull_request or workflow_dispatch; may be left out
{{- end}}
