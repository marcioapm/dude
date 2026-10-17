Restart a phase Run of this task that the delivery waits on: it is stopped, and a new Run of the same phase and category takes its place in the same step, starting fresh from the task's head, told your note. The old Run's uncommitted work is lost and its agent's context with it: to keep them, steer instead. tier runs the new Run on another of the organization's model tiers. Refused for another task's Run, one the delivery does not wait on, or one that ended.

{{- define "tools/restart_run/run" -}}
the phase Run to restart (run_…): one of this task's that the delivery waits on, still live
{{- end}}

{{- define "tools/restart_run/note" -}}
why, and what to do differently: told to the new Run
{{- end}}

{{- define "tools/restart_run/tier" -}}
a model tier of the organization's (its id or name) to run the new Run on; none keeps the role's
{{- end}}
