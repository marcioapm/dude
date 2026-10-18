{{define "refusals/delivery/restart/not-run-this-task-only-this"}}%s is not a Run of this task: only this task's Runs are restarted here{{end}}

{{- define "refusals/delivery/restart/not-phase-run-only-phase-run"}}%s is not a phase Run: only a phase Run is restarted{{end}}

{{- define "refusals/delivery/restart/has-ended-there-nothing-restart"}}%s has ended (%s): there is nothing to restart{{end}}

{{- define "refusals/delivery/restart/not-run-delivery-waits-on-only"}}%s is not a Run the delivery waits on: only a Run it waits on is restarted{{end}}

{{- define "refusals/delivery/restart/delivery-taking-step-now-restart-again"}}the delivery is taking a step now; restart %s again in a moment{{end}}

{{- define "refusals/delivery/restart/no-model-tier-tiers-are-organization"}}no model tier %q: the tiers are the organization's (in Models){{end}}

{{- define "refusals/delivery/restart/tier-names-no-model-yet-cannot"}}the tier %q names no model yet: a %s cannot run on it{{end}}
