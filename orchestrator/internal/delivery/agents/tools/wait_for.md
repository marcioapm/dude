Say what you are waiting on, then end your turn: dude checks it in its own cycle and wakes you once, when it holds or at the deadline, with what held, what did not and what else happened meanwhile, worst first. conditions: runsEnded (Runs of this task; a restarted Run counts by its replacement) and prChecksFinal (a pull request's checks passing or failing on a head, the head moving, or the pull request ending; on an open pull request each fix dude publishes moves its head: wait on the new head) and workflowEnded (a workflow run_workflow dispatched: completed, unknown, or dude cannot see it) and after (a duration from the call, at least 30m and before the deadline) and siblingTask (another task on this task's epic becoming one of becomes after the call: started, stopped, review, ready_to_merge, awaiting_human, done, failed, aborted, closed; none for any change) and prEnded (any pull request this organisation's GitHub can read merged, closed without merging, or either; one dude does not track is read from GitHub at most every 5 minutes, each read one of this task's hourly GitHub reads, and not read once they are spent) and url (a public http(s) URL answering a status, 200, 2xx or 5xx, or with not anything else, a timeout or connection error included; read at most every 2 minutes, a GET with no credentials and at most 3 redirects; URL: public addresses only). With no conditions it is a timer. One wait at a time: a new one replaces it. Waiting decides nothing, and dude's own wakes — decisions, escalations, child results, cohort digests — still come. If it already holds you are told so at once. Refused for another task's Run, pull request or dispatch, for a task off this task's epic, for a pull request GitHub does not show this organisation, and for a URL that is not public.

{{- define "tools/wait_for/conditions" -}}
up to 8, each one of: {"runsEnded": ["run_…"]}, {"prChecksFinal": {"pr": "repo#12", "head": "<full SHA; default its head now>"}} {"workflowEnded": "wfd_…"}, {"after": "45m"}, {"siblingTask": {"task": "CP-62", "becomes": ["done"]}}, {"prEnded": {"pr": "owner/repo#123", "how": "merged"}} or {"url": {"url": "https://status.example.com/health", "status": "5xx", "not": true}}
{{- end}}

{{- define "tools/wait_for/wake" -}}
all (default): when every condition holds; any: when the first does
{{- end}}

{{- define "tools/wait_for/deadline" -}}
required: a duration (45m, 2h) or a time, at most 24 hours ahead; you are woken then whatever holds
{{- end}}

{{- define "tools/wait_for/note" -}}
what you mean to do then, told back to you with the wake
{{- end}}
