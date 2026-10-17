{{define "refusals/delivery/escalations/escalation-was-just-decided-ask-nothing"}}the escalation (%s) was just decided %s: %s. Ask nothing about it; the delivery carries it out, and you are woken at the next decision{{end}}

{{- define "refusals/delivery/escalations/actions-are-for-question-about-escalation"}}actions are for a question about an escalation waiting on a person; there is none{{end}}

{{- define "refusals/delivery/escalations/task-escalation-waits-on-person-ask"}}the task's escalation waits on a person: ask its one question first (question, choices and actions, each one of %s), not %d questions; ask the rest once it is decided{{end}}

{{- define "refusals/delivery/escalations/task-escalation-waits-on-person-this"}}the task's escalation waits on a person: this question is its question. Offer choices, and actions naming what each stands for, one per choice, each one of %s; the owner picking one decides the escalation, as the banner does, except a waiver or an acceptance of a failed test, which needs the person's own words{{end}}

{{- define "refusals/delivery/escalations/not-one-this-escalation-actions"}}%q is not one of this escalation's actions: %s{{end}}

{{- define "refusals/delivery/escalations/not-choice-shipping-past-needs-person"}}%q is not a choice: shipping past %s needs the person's own reason. They %s from the banner, or answer your question in their own words{{end}}

{{- define "refusals/delivery/escalations/choices-and-read-as-same-answer"}}choices %q and %q read as the same answer (case and spaces aside): make each one distinct{{end}}

{{- define "refusals/delivery/escalations/no-escalation-waits-on-decision-now"}}no escalation waits on a decision now: decide_escalation is only for one the owner handed you with a free answer; %s{{end}}

{{- define "refusals/delivery/escalations/owner-has-not-answered-your-question"}}the owner has not answered your question about this escalation: only a person decides it. Ask them, or end your turn; %s{{end}}

{{- define "refusals/delivery/escalations/owner-answer-question-was-earlier-conductor"}}the owner's answer (question %s) was to an earlier conductor's question, not yours: ask them again yourself (ask_person, the escalation's actions as choices); %s{{end}}

{{- define "refusals/delivery/escalations/answer-your-question-not-task-owner"}}the answer to your question %s is not the task's owner's: only the owner hands you an escalation; %s{{end}}

{{- define "refusals/delivery/escalations/not-way-go-on-after-one"}}%q is not a way to go on after %s; one of %s{{end}}

{{- define "refusals/delivery/escalations/owner-answer-your-question-one-its"}}the owner's answer to your question %s is one of its choices, not their reason: to %s, they say why in their own words, or decide on the banner{{end}}

{{- define "refusals/delivery/escalations/only-task-owner-or-organisation-admin"}}only the task's owner or an organisation admin can %s this; %s{{end}}
