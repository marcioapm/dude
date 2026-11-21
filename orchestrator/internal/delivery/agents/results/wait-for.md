{{- if .Holds}}It holds already: carry on.
{{- else}}End your turn now; you are woken once, when it holds or at {{.Deadline}}. dude's own wakes (decisions, escalations, child results) still come as they do; a new wait_for replaces this one, starting any after over and counting a sibling task's changes from then.
{{- end}}
{{- with .Decision}} The delivery still waits on your decision at {{.}}; waiting decides nothing.{{end}}
