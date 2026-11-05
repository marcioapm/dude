{{define "refusals/delivery/conductor-question/current-run"}}This requires an open conductor-addressed question on a current writer Run of your live, unhalted task, and an answer to each item. Read the Run's question and answer that question only.{{end}}

{{- define "refusals/delivery/conductor-question/relay-open-writer"}}relay names a writer's question to relay to the person: it must be open, addressed to you, and asked by a current writer Run of this task (%s is not). Read the Run's question with run_chat{{end}}

{{- define "refusals/delivery/conductor-question/relay-already-open"}}%s is relayed to the person already (%s): their answer goes to the Run. End your turn and wait for it{{end}}

{{- define "refusals/delivery/conductor-question/relay-not-escalation"}}relay is for a writer's question, not a question about the escalation: ask about the escalation without relay{{end}}
