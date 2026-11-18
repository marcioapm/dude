Resolve a review thread on one of your task's pull requests, once what it asked for is done (say so in it first, with reply_on_pull_request in_reply_to the thread's comment). A fixer answering pull request feedback resolves only the threads in its feedback, named by their line-comment-… id, and not at once: held, they are resolved by dude once the fixer's published fix is on the pull request's head (never, if it is not). Refused for another pull request's thread.

{{- define "tools/resolve_thread/pr" -}}
the pull request: its number, repo#number, owner/repo#number or URL, one of your task's
{{- end}}

{{- define "tools/resolve_thread/thread" -}}
the review thread: its GitHub id (PRRT_…), or the id of a line comment in it (line-comment-…)
{{- end}}
