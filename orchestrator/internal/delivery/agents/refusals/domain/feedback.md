{{define "refusals/domain/feedback/draftids-and-submissionkey-are-required"}}draftIds and a submissionKey are required{{end}}

{{- define "refusals/domain/feedback/drafts-are-not-your-drafts-on"}}%d of the %d drafts are not your drafts on this task's current attempt: nothing was submitted{{end}}

{{- define "refusals/domain/feedback/submission-key-was-used-for-another"}}submission key %q was used for another batch: use a new key for new drafts{{end}}

{{- define "refusals/domain/feedback/this-task-done-feedback-on-goes"}}this task is done: feedback on it goes in a follow-up task. Your drafts are kept.{{end}}

{{- define "refusals/domain/feedback/this-task-recover-first-then-submit"}}this task is %s: Recover it first, then submit. Your drafts are kept.{{end}}

{{- define "refusals/domain/feedback/this-task-delivery-has-ended-start"}}this task's delivery has ended: start it over to give it feedback. Your drafts are kept.{{end}}

{{- define "refusals/domain/feedback/feedback-not-found"}}feedback %s not found{{end}}

{{- define "refusals/domain/feedback/feedback-on-attempt-which-read-only"}}feedback %s is on attempt %d, which is read-only{{end}}

{{- define "refusals/domain/feedback/reply-needs-words"}}a reply needs words{{end}}

{{- define "refusals/domain/feedback/feedback-resolved-by-commit-not-yet"}}feedback %s is resolved by a commit not yet published: reopen it to ask for more, or resolve it to confirm{{end}}

{{- define "refusals/domain/feedback/feedback-only-resolved-or-replied-item"}}feedback %s is %s: only a resolved or replied item is reopened{{end}}

{{- define "refusals/domain/feedback/say-why-reply-needs-words"}}say why: a reply needs words{{end}}

{{- define "refusals/domain/feedback/feedback-there-nothing-reply-until-person"}}feedback %s is %s: there is nothing to reply to until a person reopens it{{end}}

{{- define "refusals/domain/feedback/feedback-not-your-task"}}feedback %s is not your task's{{end}}

{{- define "refusals/domain/feedback/commit-not-commit-sha-resolve-with"}}commit %q is not a commit SHA: resolve with the commit that fixed it (`--commit HEAD`){{end}}

{{- define "refusals/domain/feedback/feedback-already"}}feedback %s is %s already{{end}}

{{- define "refusals/domain/feedback/only-implementer-or-fixer-whose-commit"}}only the implementer or fixer whose commit fixed it resolves feedback; reply instead, or start a fix{{end}}

{{- define "refusals/domain/feedback/feedback-on-resolve-with-commit-there"}}feedback %s is on %s: resolve it with a commit there, not in %s{{end}}

{{- define "refusals/domain/feedback/not-repository-this-task-changes"}}%s is not a repository this task changes (%s){{end}}

{{- define "refusals/domain/feedback/this-task-changes-name-repository-your"}}this task changes %s: name the repository your commit is in (--repo){{end}}

{{- define "refusals/domain/feedback/at-most-20-ids-at-time"}}at most 20 ids at a time{{end}}
