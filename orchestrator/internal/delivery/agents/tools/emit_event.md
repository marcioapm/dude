Record an event on your run for the people following it: progress (type progress, data like {"done": 3, "of": 10, "step": "tests"}), a milestone, a measurement. It shows in your chat and the run's events.

{{- define "tools/emit_event/type" -}}
what kind of event: lowercase words and dots, like progress or tests.finished
{{- end}}

{{- define "tools/emit_event/data" -}}
anything to go with it: an object like {"done": 3, "of": 10} (at most 16 KB)
{{- end}}
