{{define "refusals/tools/github/this-organisation-has-no-github-connection"}}this organisation has no GitHub connection: nothing on GitHub can be read{{end}}

{{- define "refusals/tools/github/not-repository-you-may-read-have"}}%s is not a repository you may read: %s have none on GitHub{{end}}

{{- define "refusals/tools/github/not-repository-you-may-read-only"}}%s is not a repository you may read: only %s, %s{{end}}

{{- define "refusals/tools/github/at-most-github-reads-hour-this"}}at most %d GitHub reads an hour; this session has made them: wait, or work with what you read{{end}}

{{- define "refusals/tools/github/kind-issue-or-pr"}}kind is issue or pr{{end}}

{{- define "refusals/tools/github/state-open-closed-or-all"}}state is open, closed or all{{end}}

{{- define "refusals/tools/github/query-at-most-256-characters"}}query: at most 256 characters{{end}}

{{- define "refusals/tools/github/name-repository-in-repo-not-with"}}name the repository in repo, not with %s in the query{{end}}

{{- define "refusals/tools/github/number-issue-from-1"}}number is the issue's, from 1{{end}}

{{- define "refusals/tools/github/number-pull-request-from-1"}}number is the pull request's, from 1{{end}}

{{- define "refusals/tools/github/files-at-most-paths-at-once"}}files: at most %d paths at once{{end}}

{{- define "refusals/tools/github/check-id-check-run-from-1"}}checkId is the check run's id, from 1{{end}}

{{- define "refusals/tools/github/run-id-run-from-1-or-its-link"}}runId is the run's id, from 1, or its link (https://github.com/owner/name/actions/runs/<id>){{end}}

{{- define "refusals/tools/github/run-link-is-another-repository"}}%s is a run of another repository than %s: leave repo out, or name the link's{{end}}
