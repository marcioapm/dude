{{define "refusals/domain/coordination/say-something-text-required"}}say something: text is required{{end}}

{{- define "refusals/domain/coordination/message-at-most-kib-say-less"}}a message is at most %d KiB: say less, or pin a note{{end}}

{{- define "refusals/domain/coordination/no-run-will-read-this-carry"}}%s is %s: no Run will read this. Carry on with your assumption, or ask a person.{{end}}

{{- define "refusals/domain/coordination/refused-this-run-has-sent-messages"}}Refused: this Run has sent %d messages in the last hour, the most a Run may send. The next is allowed at %s UTC. Until then, `dude task list --peers`, `dude diff --task %s` and the epic notes may already answer it; if they don't, carry on and ask then.{{end}}

{{- define "refusals/domain/coordination/no-message"}}no message %s{{end}}

{{- define "refusals/domain/coordination/not-message-between-your-task-and"}}%s is not a message between your task and %s: a reply goes to the other task of its thread{{end}}
