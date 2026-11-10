Decide an offered non-person-only escalation action directly, without an owner answer: resume, retry, wait or stop, with your note. You may explicitly extend stuck, exhausted or not_converging with further rounds and direction; no fixed retry ceiling. A retry on pr_loop_exhausted needs your reason, different from any earlier retry's. Accepting past blocking findings, waivers, amending criteria, done on pull_request_closed and all conductor_unavailable actions require the owner-authorized question/answer path. Ask one question with exactly one recommended choice and a proposed reason where required; the owner's pick decides directly. For a valid free-text owner answer, use their words for a person-only decision. Allowed once per escalation.

{{- define "tools/decide_escalation/action" -}}
one of the escalation's actions; for not_converging, retry fixes differently, accept accepts only the named risks, and amend replaces the named findings' criteria with the person's words before fixing; for conductor_unavailable, retry waits for your decisions again and accept lets Deliver's defaults finish the delivery
{{- end}}

{{- define "tools/decide_escalation/note" -}}
what you decided and why, kept with the task for later agents; amend uses the person's words as the replacement criterion
{{- end}}
