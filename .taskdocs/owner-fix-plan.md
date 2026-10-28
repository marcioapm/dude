# Owner-note lifecycle fix plan

Scope: only the three owner-note defects at pinned 65fb4d685475f92f9919b043abe72927ee9d1691. No queued base-sync work, no delegation, no PR.

1. Trace workflow note consumption, decision dispatch, restart and phase replacement paths and existing DB fixtures.
2. Prepare pgvector/pgvector:pg17-trixie PostgreSQL with tmpfs and fixture credentials (dude:dude, localhost:5433). Run bun install --frozen-lockfile before migrations. Keep temporary/cache/workspace files in /workspace/tmp.
3. Add production-path DB regressions first: both waiver scopes with pending re-parks and actual downstream input; wait early dispatch with feedback; replacement preserving multiline attribution before directive delivery.
4. Record red results, implement the smallest coherent fix, and commit locally as work progresses.
5. Run isolated regression mutations, restore, then clean targeted delivery/restart/phases/API tests, build/vet/typecheck and diff guards/test-ID comparison. Long gates run detached with logs; report counts/failures only. Full root gate only with >30 minutes remaining.
6. Keep owner-fix-report.md current with dispositions, exact results, commits and omitted/failed gates.
