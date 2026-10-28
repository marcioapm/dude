Post a short message in the chat thread this session was started from, as dude, through the organisation's chat installation. The thread is this session's own; you name no channel. Write plain text with *bold*, _italic_ and `code`; paste links bare. At most 3 posts per turn (everything after a person's latest message) and 3,000 characters a post; never a special mention (<!channel>, <!here>, <!everyone>, a user group). People outside the organisation may read the thread: post answers, decisions and links, never logs, code or tool output. It returns the post's permalink.

{{- define "tools/session/reply_in_thread/text" -}}
the message, 1 to 3000 characters of plain text with *bold*, _italic_ and `code`: a short answer, or what was created with its link
{{- end}}
