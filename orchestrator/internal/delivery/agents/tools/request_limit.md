Ask the task's people to raise one of its delivery's limits, for this task alone, when you hit it and the work needs more: the pull request fix rounds per review round (perReviewRound, the delivery policy's), the fix rounds per pull request (perPullRequest, the organisation's GitHub settings'), or one more failed-check rerun on a pull request's head (rerun). dude asks them with your reason and the choices to allow it or not; only someone who may change that setting can approve (an organisation admin for the fix rounds; the task's owner or an admin for a rerun). Approved, the check allows exactly what was granted, and limits shows it; the setting itself does not change. It is your one open question: end your turn after it, the answer is your next message. One request per limit at a time.

{{- define "tools/request_limit/limit" -}}
perReviewRound, perPullRequest or rerun
{{- end}}

{{- define "tools/request_limit/to" -}}
for perReviewRound and perPullRequest: the limit you ask for, above what limits shows now, by at most 10
{{- end}}

{{- define "tools/request_limit/pr" -}}
for rerun: the pull request, as repo#number or number; the task's only open one when left out
{{- end}}

{{- define "tools/request_limit/reason" -}}
why the task needs it, in a sentence or two the person decides on (at most 2000 characters)
{{- end}}
