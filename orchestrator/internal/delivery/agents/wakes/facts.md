{{define "wakes/facts/checkout"}}
{{- if eq .Case "off-branch"}}{{.Repo}}: your checkout is {{.Behind}} behind and not on the task branch; `git switch -C {{.Ref}} lux/{{.Ref}}` to take it.
{{- else if eq .Case "kept"}}{{.Repo}}: your checkout is {{.Behind}} behind and {{join .Why " and "}}; `git merge lux/{{.Ref}}` to take them in.
{{- else if eq .Case "ahead"}}{{.Repo}}: your checkout is {{.Ahead}} ahead of the task branch and not behind: publish, or keep working.
{{- else if eq .Case "failed"}}{{.Repo}}: your checkout could not be brought current ({{clip (oneLine .Error) 120}}); it is as you left it.
{{- else}}{{.Repo}}: your checkout is {{.Behind}} behind and {{.Ahead}} ahead ({{.Status}}); `git merge lux/{{.Ref}}` to take the task branch in.
{{- end}}{{end}}

{{- define "wakes/facts/checkout-dirty"}}has local changes{{end}}

{{- define "wakes/facts/checkout-own"}}{{.}} commits of its own{{end}}

{{- define "wakes/facts/checkout-kept-as-is"}}was kept as it is{{end}}

{{- define "wakes/facts/operation"}}{{.Repo}}: {{template "wakes/facts/operation-name" .Op}} is in progress in your checkout ({{.Behind}} behind the task branch): {{template "wakes/facts/operation-finish" .Op}}{{if .Behind}}; then `git merge lux/{{.Ref}}`{{end}}.{{end}}

{{- define "wakes/facts/operation-finish"}}
{{- if or (eq . "rebase") (eq . "am") (eq . "merge") (eq . "cherry-pick") (eq . "revert")}}resolve and `git {{.}} --continue`, or `git {{.}} --abort`
{{- else if eq . "sequencer"}}resolve and `git cherry-pick --continue` (or `git revert --continue`), or `git cherry-pick --abort` (or `git revert --abort`)
{{- else}}finish or abort it
{{- end}}{{end}}

{{- define "wakes/facts/operation-name"}}
{{- if eq . "sequencer"}}a cherry-pick or revert of several commits
{{- else if eq . "am"}}a `git am`
{{- else if eq . ""}}a git operation
{{- else}}a {{.}}
{{- end}}{{end}}

{{- define "wakes/facts/review-round"}}Review round {{.Iteration}} of {{.Max}} done: {{.Open}} open findings{{with .Severities}} ({{join . ", "}}){{end}}{{with .People}}, and {{.}} people's feedback items to handle (feedback){{end}}.{{end}}

{{- define "wakes/facts/outcome"}}{{.Phase}} {{.RunID}} {{if .Succeeded}}finished{{else}}failed{{end}}{{with .Commits}}, at {{join . " "}}{{end}}, {{.Files}} files changed.{{end}}

{{- define "wakes/facts/nothing-to-add"}} Nothing to add: the work it continues is reviewed as it is.{{end}}

{{- define "wakes/facts/pr-feedback"}}{{.Count}} actionable items ({{join .Kinds ", "}}): read them with pull_requests.{{end}}

{{- define "wakes/facts/failing-check"}}failing check{{end}}

{{- define "wakes/facts/took-over"}}Took over {{.Head}} at {{.SHA}}, {{.Files}} files changed against {{.Base}}. {{if .Continues}}Saved as work to continue: an implementer carries it on.{{else}}Saved as work to review as it is.{{end}}{{end}}

{{- define "wakes/facts/test-failed"}}test {{.RunID}} failed: criteria {{.Criteria}}, {{.Findings}} blocking findings of the tester's; test fix round {{.Round}} of {{.Max}}.{{end}}

{{- define "wakes/facts/test-passed"}}test {{.}}: every criterion passed or was settled by a person.{{end}}

{{- define "wakes/facts/feedback-open"}}
{{- if eq .When "after-fix"}}{{.N}} feedback items from people are still open after the fix: read them with feedback.
{{- else if eq .When "after-integration"}}{{.N}} feedback items from people are open after the base integration: read them with feedback.
{{- else if eq .When "before-pr"}}{{.N}} feedback items from people are open (feedback): they go to a fixer before any pull request.
{{- else if eq .When "ready-to-read"}}{{.N}} feedback items from people on the work ready to read: read them with feedback.
{{- else}}{{.N}} feedback items from people are open: read them with feedback.
{{- end}}{{end}}

{{- define "wakes/facts/untested-head"}}the last commit is the conductor's; run a review first{{end}}

{{- define "wakes/facts/point-start"}}start: nothing has run yet{{end}}

{{- define "wakes/facts/point-after_implement"}}after implement{{end}}

{{- define "wakes/facts/point-after_review"}}after a review round{{end}}

{{- define "wakes/facts/point-after_fix"}}after a fix{{end}}

{{- define "wakes/facts/point-before_pull_request"}}before the pull request{{end}}

{{- define "wakes/facts/point-pull_request_feedback"}}pull request feedback{{end}}
