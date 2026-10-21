Your task's review findings, open and most severe first: each one's id, severity, category, file:line, status and how it was settled (fixed by which Run, accepted by a person, or open after so many fix attempts). Name ids (at most 20) to read those in full: title, description, suggested fix, acceptance criterion and the resolution note.

{{- define "tools/findings/ids" -}}
findings to read in full (fnd_…), with their text; empty lists the task's findings without it
{{- end}}
