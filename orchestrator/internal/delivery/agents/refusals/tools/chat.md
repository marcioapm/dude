{{define "refusals/tools/chat/project-hidden"}}%s is hidden from what is started from %s: this session or task can't see it, its repositories, tasks or memory{{end}}

{{- define "refusals/tools/chat/history-before-or-after"}}page with before or with after, not both{{end}}

{{- define "refusals/tools/chat/history-not-from-chat"}}this session was not started from a chat: there is no conversation to read{{end}}

{{- define "refusals/tools/chat/history-not-installed"}}the chat this session came from is no longer connected to dude: its conversation can't be read; ask a member what it said{{end}}

{{- define "refusals/tools/chat/history-unavailable"}}the chat did not answer: try again in a while, or work with what you have{{end}}

{{- define "refusals/tools/chat/history-too-far"}}that message is too far back or ahead to page to from here: use a nearer message's id, from a page you read{{end}}

{{- define "refusals/tools/chat/history-at-most-reads-hour"}}at most %d chat reads an hour; this session has made them: wait, or work with what you read{{end}}
