Start a durable cohort of 1–8 independent child agents atomically. Uses start_agent profiles, briefs, notes and tier overrides with the same admission guards. Any refusal creates no children. Receive one digest after every current member settles, including failures and aborts, not one input per member. Conductor only; never bypasses fixed review policy.

{{- define "tools/start_agents/runs" -}}
1–8 complete child briefs, each with profile, prompt, note and optional tier; per-member keys are unsupported, use the cohort key
{{- end}}
{{- define "tools/start_agents/key" -}}
idempotency key scoped to this parent Run; replay returns persisted membership
{{- end}}
