Read a check's job log, failed or not. Without match: a bounded failure excerpt, or what its check run reported when it is not a GitHub Actions job or GitHub cannot provide its log. The excerpt can retain failure sites from the middle of the log as well as its end; omission markers mean intervening log text was left out, not that no other failures occurred. Use the check id from github_pull_request (or a job id from github_run_jobs or github_branch_runs) before deciding whether to rerun or fix it. To check that a specific test or step passed, or to find any line, pass match: the job's whole log is searched and the matching lines come back with their line numbers and the lines around them, and how many matched in all. Read only; counts against the hourly GitHub reads; the result is marked as external text. External-text marking is not secret redaction: this tool does not load credentials to redact returned logs; caller-supplied known-secret redaction is separate.

{{- define "tools/github_check_log/repo" -}}
the repository, owner/name, one of your projects'
{{- end}}

{{- define "tools/github_check_log/checkId" -}}
the check id from github_pull_request, or a job id from github_run_jobs or github_branch_runs
{{- end}}

{{- define "tools/github_check_log/match" -}}
what to find in the job's whole log: an RE2 regular expression, case-insensitive, at most 200 characters (plain text matches itself; escape ( [ . * + ? with \ to match them as written), like test_goal_with_nul.*PASSED or \d+ passed
{{- end}}

{{- define "tools/github_check_log/context" -}}
with match: lines shown before and after each matching line, 0 to 5; 2 when left out
{{- end}}
