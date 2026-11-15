{{- define "refusals/delivery/ask-author/review-only"}}ask_author is a review-only task's: here the pull request is dude's own work, and a question about it goes to the person (ask_person){{end}}
{{- define "refusals/delivery/ask-author/question-required"}}Say what to ask the author: question is required{{end}}
{{- define "refusals/delivery/ask-author/too-long"}}Ask one thing, in at most %d bytes{{end}}
{{- define "refusals/delivery/ask-author/unknown-author"}}dude does not know who opened pull request #%d, so it cannot ask them: ask the owner (ask_person) instead{{end}}
{{- define "refusals/delivery/ask-author/no-thread"}}%s has no thread of dude's on the pull request (findings lists those dude commented on inline): ask without finding_id, on the conversation{{end}}
{{- define "refusals/delivery/ask-author/github-refused"}}GitHub did not take the question (%v): nothing was posted, and the question is withdrawn{{end}}
