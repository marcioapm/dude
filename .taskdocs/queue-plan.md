# Queued base sync plan

Scope: Bug 2 only; preserve owner notes/person_note and candidate review gates. No simplification redesign, delegation, PR or push.

## Storage decision
Read workflow/runtime.go claim/advance/keepOwned: steps read a snapshot, replace non-owned state, and retire claimed signals on transition commit. A PR-feedback signal alone is durable but its consumption is not proof of integration. Use a dedicated tenant-scoped request table, independent of workflow state, keyed by PR and requested base SHA. Keep requests pending until trusted forge ancestry proves the requested base is in the public PR head; queuing a directive is not completion. Unknown/unavailable forge retains pending work. Signals are wakeups only, not request ownership.

## Sequence
1. Inspect delivery/runtime, capability, prompt, API and PR UI paths.
2. Start real pgvector Postgres; frozen Bun install before migrations. Add regression tests first and save red output.
3. Persist requests transactionally with normal owner auth and live-task checks. Deduplicate same base, not same PR head. Route a single directive to a capable live writer, or include it in the next writer briefing. Never brief reviewers/QA to write, never launch a parallel writer or resurrect stopped work.
4. Reconcile completion using forge comparison; show queued/resolving/complete truthfully, without claiming candidate publication.
5. Focused Go delivery/API/phases, touched Bun tests, typecheck, vet, diff checks, diff guard and collected IDs. Add/run a Python browser e2e with pinned Chromium and explicit executable path. Save full outputs in .taskdocs logs, report exact red/green results and skipped gates.

Commit coherent increments approximately every 20 minutes. Reports: .taskdocs/queue-report.md and /workspace/out/report.md. Full root suite only with at least 30 minutes remaining.
