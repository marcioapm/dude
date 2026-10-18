{{define "refusals/domain/relations-write/task-can-t-be-related-itself"}}a task can't be related to itself{{end}}

{{- define "refusals/domain/relations-write/and-are-already-related"}}%s and %s are already related{{end}}

{{- define "refusals/domain/relations-write/that-would-make-loop-task-can"}}that would make a loop: %s. A task can't wait on itself{{end}}

{{- define "refusals/domain/relations-write/has-no-relation-with"}}%s has no %s relation with %s{{end}}

{{- define "refusals/domain/relations-write/you-can-remove-only-relation-this"}}you can remove only a relation this task's agents added; ask %s, its owner{{end}}
