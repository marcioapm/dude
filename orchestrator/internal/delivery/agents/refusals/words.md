{{define "refusals/words/mid-operation"}}{{.Repo}}: {{template "wakes/facts/operation-name" .Op}} is in progress in your checkout: finish or abort it, then publish.{{end}}

{{- define "refusals/words/escalation-note"}}The conductor proposed: {{clip (oneLine .Proposal) 1500}}
The owner answered: {{clip (oneLine .Answer) 1000}}{{with .Decided}}
The conductor decided: {{clip (oneLine .) 1000}}{{end}}{{end}}

{{- define "refusals/words/decided-by"}}{{if .Conductor}}by the conductor {{.Conductor}} (decide_escalation){{else if .QuestionID}}by the owner's answer to question {{.QuestionID}}{{else}}by a person on the banner{{end}}{{end}}

{{- define "refusals/words/blocked"}}{{.Task}} is blocked by {{.Blockers}}: it can start once {{if eq .N 1}}that's{{else}}they're{{end}} merged.{{end}}

{{- define "refusals/words/blocked-agent"}}{{.}} You can't start it anyway; a person can. Tell the person, and end your turn: you are woken when it can start.{{end}}

{{- define "refusals/words/blocker"}}{{.Key}} ({{.State}}){{end}}

{{- define "refusals/words/not-a-peer"}}{{.}} is not one of your peers: only the other tasks on your epic are (list_tasks with peers: true shows them){{end}}

{{- define "refusals/words/relation-line"}}{{with .Open}}blocked by {{join . ", "}}{{end}}{{if and .Open .Blocks}} · {{end}}{{with .Blocks}}blocks {{join . ", "}}{{end}}{{if and (or .Open .Blocks) .Related}} · {{end}}{{with .Related}}related {{join . ", "}}{{end}}{{if and (or .Open .Blocks .Related) .DuplicateOf}} · {{end}}{{with .DuplicateOf}}duplicate of {{.}}{{end}}{{if and (or .Open .Blocks .Related .DuplicateOf) .DuplicatedBy}} · {{end}}{{with .DuplicatedBy}}duplicated by {{join . ", "}}{{end}}{{end}}

{{- define "refusals/words/source-changed"}}GitHub's pull request (or branch) no longer matches what this task adopted: {{.}}. Put it back on GitHub and Try again, or stop this task and adopt it in a new one.{{end}}

{{- define "refusals/words/writer"}}an implementer is working on this task; wait for it, or steer it{{end}}

{{- define "refusals/words/policy"}}Deliver decides this task: steer its agents, or ask the person to hand it to you.{{end}}

{{- define "refusals/words/candidate"}}a fix for the pull request is being reviewed and tested; publish once it has reached the pull request{{end}}

{{- define "refusals/words/behind"}}your checkout is behind the task branch; `git merge lux/%s` first{{end}}

{{- define "refusals/words/nothing"}}nothing to publish: you have no commits on top of the task branch's head{{end}}

{{- define "refusals/words/too-big"}}this is %d lines in %d files, past the conductor's limit of %d lines / %d files: delegate this (start_phase implement){{end}}

{{- define "refusals/words/binary"}}%s has a change GitHub shows no lines for (a binary file, a rename or mode change with no edits, or a diff too large to show), past the conductor's limit: delegate this (start_phase implement){{end}}

{{- define "refusals/words/publish-landing"}}
{{- if eq .Case "not-conductor"}}you are no longer this task's conductor
{{- else if eq .Case "read-only"}}your checkout is read-only for this conversation: {{.What}}
{{- else if eq .Case "no-delivery"}}this task has no delivery in progress
{{- else if eq .Case "ended"}}this task is {{.What}}
{{- else}}the task was started over since you published
{{- end}}{{end}}

{{- define "refusals/words/not-moved-refused"}}{{.Repo}} was refused: {{clip (oneLine .Why) 160}}{{end}}

{{- define "refusals/words/not-moved-stalled"}}{{.Repo}} could not be confirmed at {{.Head}}{{end}}
