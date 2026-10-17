{{define "results/machine-refused/said"}}
{{- if and (eq .Status 400) .OtherPool}}this lux cannot move a Run between pools yet.
{{- else if eq .Status 400}}this lux cannot change a stopped Run's CPUs or memory.
{{- else if eq .Code "unknown_pool"}}{{.Size}}'s pool has no host it fits
{{- else if and (eq .Code "platform_mismatch") .PoolPlatforms .ImagePlatforms}}{{.Size}} is in {{template "results/machine-refused/in" .Pool}}, whose machines are {{join .PoolPlatforms ", "}}; this agent's image is {{join .ImagePlatforms ", "}} only.
{{- else if eq .Code "platform_mismatch"}}{{.Size}} is in {{template "results/machine-refused/in" .Pool}}, whose machines cannot run this agent's image.
{{- else if and (eq .Code "pool_unfit") .Nested}}{{.Size}} is in {{template "results/machine-refused/in" .Pool}}, which can't run nested containers, and this agent's image needs them.
{{- else if eq .Code "pool_unfit"}}{{.Size}} ({{.CPUs}} CPUs · {{.Memory}} GiB) is larger than any machine in {{template "results/machine-refused/in" .Pool}}.
{{- else if eq .Code "invalid_spec"}}lux cannot give this agent {{.Size}} as the size is set up.
{{- else if eq .Code "not_resumable"}}lux was already resuming this Run, so it could not move it to {{.Size}}.
{{- else}}lux would not move this agent to {{.Size}}.
{{- end}}{{end}}

{{- define "results/machine-refused/advice"}}
{{- if and (eq .Status 400) .OtherPool}} Ask for a size in your own pool.
{{- else if eq .Status 400}} Use fewer workers or less data.
{{- else if eq .Code "unknown_pool"}}; ask for another size
{{- else if and (eq .Code "platform_mismatch") .PoolPlatforms .ImagePlatforms}} Ask for another size, or ask a person to add {{if .Vowel}}an{{else}}a{{end}} {{.Arch}} image.
{{- else if eq .Code "platform_mismatch"}} Ask for another size, or ask a person to add an image for them.
{{- else if eq .Code "invalid_spec"}} Ask for another size, or ask a person to check {{.Size}} in Machines.
{{- else if eq .Code "not_resumable"}} Ask again once you are running.
{{- else}} Ask for another size.
{{- end}}{{end}}

{{- define "results/machine-refused/in"}}{{with .}}{{.}}{{else}}its pool{{end}}{{end}}
