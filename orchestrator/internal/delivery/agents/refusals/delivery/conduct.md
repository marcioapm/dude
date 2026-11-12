{{define "refusals/delivery/conduct/this-task-you-are-read-only"}}this task is %s: you are read-only now and start nothing. Offer to record the change as a follow-up task (create_task), linked to this one, and create it once the person agrees{{end}}

{{- define "refusals/delivery/conduct/this-task-has-no-delivery-in"}}this task has no delivery in progress to decide for{{end}}

{{- define "refusals/delivery/conduct/deliver-takes-this-task-decisions-not"}}Deliver takes this task's decisions, not you: you are read-only. A person hands them to you by writing in Chat; until then, answer and advise{{end}}

{{- define "refusals/delivery/conduct/dude-holds-this-delivery-before-its-pr"}}dude holds this delivery before its pull request for the person's Open or Draft: you decide nothing here. If they ask, tell them to answer Open or Draft{{end}}

{{- define "refusals/delivery/conduct/this-decision-was-taken-already-you"}}this decision was taken already (%s); you are woken at the next one{{end}}

{{- define "refusals/delivery/conduct/delivery-stopped-for-person-at-escalation"}}the delivery is stopped for a person at an escalation (%s), not waiting on your decision. The person decides it on the banner, or by answering your question about it (ask_person with the escalation's actions as choices); if the owner answers that question in their own words, decide it with decide_escalation{{end}}

{{- define "refusals/delivery/conduct/delivery-not-waiting-on-decision-now"}}the delivery is not waiting on a decision now (it is at %s): you are woken when it is{{end}}

{{- define "refusals/delivery/conduct/you-are-no-longer-this-task"}}you are no longer this task's conductor: another took over from you. Decide nothing{{end}}

{{- define "refusals/delivery/conduct/cannot-start-at-this-decision-can"}}%s cannot start at this decision (%s); it can start %s{{end}}

{{- define "refusals/delivery/conduct/your-publish-moving-task-branch-now"}}your publish is moving the task branch now; start %s again in a moment{{end}}

{{- define "refusals/delivery/conduct/categories-are-for-review-findings-for"}}categories are for review, findings for fix{{end}}

{{- define "refusals/delivery/conduct/no-reviewer-categories-are"}}no reviewer %q: the categories are %s{{end}}

{{- define "refusals/delivery/conduct/then-is-for-fix-or-simplify"}}then is for fix or simplify, not %s: start the review on its own (start_phase review){{end}}

{{- define "refusals/delivery/conduct/then-names-the-review"}}then needs review: the reviewers to run after the fix (correctness, tests, …){{end}}

{{- define "refusals/delivery/conduct/then-wake-is-one-of"}}then.wake %q: it is after_review (the default) or after_fix{{end}}

{{- define "refusals/delivery/conduct/round-is-for-review-fix-or-simplify"}}round is for review, fix or simplify, not %s{{end}}

{{- define "refusals/delivery/conduct/round-scope-is-one-of"}}round.scope %q: it is whole or fix{{end}}

{{- define "refusals/delivery/conduct/no-open-finding-or-feedback-item"}}no open finding or feedback item to fix{{end}}

{{- define "refusals/delivery/conduct/not-finding-this-task-delivery-findings"}}%s is not a finding of this task's delivery (findings lists them){{end}}

{{- define "refusals/delivery/conduct/not-open"}}%s is %s, not open{{end}}

{{- define "refusals/delivery/conduct/pull-request-fixes-are-at-their"}}pull request fixes are at their bound: %d of %d this review round. At a decision, `decide next` escalates pr_loop_exhausted to you; or ask the person how to go on (ask_person){{end}}

{{- define "refusals/delivery/conduct/pull-request-in-has-had-fix"}}the pull request in %s has had %d fix rounds, the organization's bound: at a decision, `decide next` escalates pr_loop_exhausted to you; or ask the person how to go on (ask_person){{end}}

{{- define "refusals/delivery/conduct/not-decision-at-one-or-start"}}%q is not a decision at %s; one of %s, or start_phase{{end}}

{{- define "refusals/delivery/conduct/feedback-items-from-people-are-not"}}%d feedback items from people are not handled yet (feedback): start a fix for them (start_phase fix) before the pull request{{end}}

{{- define "refusals/delivery/conduct/you-already-have-question-waiting-for"}}you already have a question waiting for an answer: end your turn and wait for it{{end}}

{{- define "refusals/delivery/conduct/say-what-ask-person-in-note"}}say what to ask the person in the note{{end}}

{{- define "refusals/delivery/conduct/person-has-not-been-asked-decide"}}the person has not been asked: decide ask_person first, which asks them %q{{end}}

{{- define "refusals/delivery/conduct/person-has-not-answered-yet-end"}}the person has not answered %q yet: end your turn and wait{{end}}

{{- define "refusals/delivery/conduct/person-answered-at-earlier-head-work"}}the person answered at an earlier head; the work has moved since. Ask again (decide ask_person){{end}}

{{- define "refusals/delivery/conduct/person-answered-not-open-or-draft"}}the person answered %q, not Open or Draft: do what they asked{{end}}

{{- define "refusals/delivery/conduct/say-why-dismissed-reason-shown-with"}}say why it is dismissed: the reason is shown with the finding{{end}}

{{- define "refusals/delivery/conduct/not-finding-this-task-findings-lists"}}%s is not a finding of this task (findings lists them){{end}}

{{- define "refusals/delivery/conduct/already"}}%s is %s already{{end}}

{{- define "refusals/delivery/conduct/implementer-has-started-on-this-attempt"}}an implementer has started on this attempt: the task's text is what it was given. Record what changed as a decision with the person (ask_person), or as a follow-up task; once they have answered, the acceptance criteria may take their answer, naming that question (questionId){{end}}

{{- define "refusals/delivery/conduct/question-is-not-persons-answer-here"}}%s is not a question of this task a person has answered: after the implementer has started, update_task writes only a person's answer into the acceptance criteria, naming their answered question{{end}}

{{- define "refusals/delivery/conduct/answer-changes-criteria-only"}}after the implementer has started, a person's answer changes the acceptance criteria only, not the goal{{end}}

{{- define "refusals/delivery/conduct/give-goal-acceptance-criteria-or-both"}}give a goal, acceptance criteria, or both{{end}}

{{- define "refusals/delivery/conduct/nothing-was-saved"}}%s; nothing was saved{{end}}

{{- define "refusals/delivery/conduct/answer-or-withdraw-review-question"}}give an answer, or withdraw: true with a reason; one of the two{{end}}

{{- define "refusals/delivery/conduct/options-only-in-prose"}}the note lists the answers as prose (%q): give them as choices (label, and action open or draft for a choice that opens the pull request), so the person can pick one{{end}}

{{- define "refusals/delivery/conduct/choices-need-two"}}offer at least two choices, or none for a question answered in the person's own words{{end}}

{{- define "refusals/delivery/conduct/choice-action-is-one-of"}}choice %q: action %q is one of open or draft (it opens the pull request, as the person picked), or none{{end}}

{{- define "refusals/delivery/conduct/gate-choices-are-fixed"}}before the pull request the choices are fixed (%s): say which you recommend with recommend, not choices{{end}}

{{- define "refusals/delivery/conduct/recommend-is-one-of"}}recommend %q: it is one of %s{{end}}

{{- define "refusals/delivery/conduct/answer-no-longer-authorizes"}}the person's answer to %s no longer authorizes the opening: %s. Ask again (decide ask_person){{end}}

{{- define "refusals/delivery/conduct/answer-kept-open-before-pr"}}the person's answer to %s is kept: open_pull_request is decided at the decision before the pull request, where it is cited if it still holds. This decision (%s) is one of %s; do not ask the person again{{end}}

{{- define "refusals/delivery/conduct/choices-are-for-ask-person"}}choices and recommend are for decide ask_person{{end}}
