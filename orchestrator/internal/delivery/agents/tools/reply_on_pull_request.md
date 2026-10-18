Answer on one of your task's pull requests, as dude's GitHub login: for a question that came from there (a Chat message from a GitHub person names the pull request and the comment). in_reply_to a line-comment-… answers in its review thread; any other comment, or none, posts on the pull request's conversation, quoting the first line of the comment answered. It changes nothing and wakes nobody. Refused for another task's pull request; GitHub refusing it is said, and nothing is posted. A fixer answering pull request feedback replies only to the comments in its feedback, in_reply_to each.

{{- define "tools/reply_on_pull_request/pr" -}}
the pull request: its number, or repo#number, one of your task's
{{- end}}

{{- define "tools/reply_on_pull_request/text" -}}
what to post, in Markdown, as you would say it in Chat
{{- end}}

{{- define "tools/reply_on_pull_request/in_reply_to" -}}
the comment you answer (issue-comment-…, line-comment-… or review-…): a line comment is answered in its thread; anything else is quoted
{{- end}}
