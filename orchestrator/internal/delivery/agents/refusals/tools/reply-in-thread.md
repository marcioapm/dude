{{define "refusals/tools/reply-in-thread/not-from-chat"}}this session was not started from a chat thread: there is no thread to reply in{{end}}

{{- define "refusals/tools/reply-in-thread/text-length"}}a thread reply is 1 to %d characters; this one is %d: summarise it in a few lines and point to the session or task link for the rest{{end}}

{{- define "refusals/tools/reply-in-thread/broadcast"}}a thread reply never notifies a whole channel: remove %s and post again{{end}}

{{- define "refusals/tools/reply-in-thread/per-turn"}}at most %d thread replies per turn; this turn has posted them: say the rest in this session, and post again after the next message{{end}}

{{- define "refusals/tools/reply-in-thread/not-connected"}}the chat this session came from is no longer connected to dude: nothing was posted; answer in this session{{end}}

{{- define "refusals/tools/reply-in-thread/busy"}}the chat did not take the reply (%s): nothing was posted; try again in a minute{{end}}

{{- define "refusals/tools/reply-in-thread/refused"}}the chat refused the reply: %s. Nothing was posted; answer in this session{{end}}
