# Owner-note lifecycle fix report

Base: 65fb4d685475f92f9919b043abe72927ee9d1691.
Scope: three owner-note lifecycle findings only; queued base-sync excluded. Working alone; no PR.

## Dispositions
- Valid: shared park consumed waiver notes on pending-only waits. Consumption moved to the six successful phase-start exits, after review fan-out finishes; zero-review paths retain the note.
- Valid: RestartRunTx omitted person_note. It now reads and copies it into PhaseRun, serving both replaceRound and retireForSecrets.
- Valid: wait early returns bypassed note assignment. Handoff now precedes action dispatch; waitForPR still clears genuine waiting notes.

## Evidence and gates
- Real PostgreSQL: podman owner-notes-pg, pgvector/pgvector:pg17-trixie, tmpfs /var/lib/postgresql/data, localhost:5433, dude:dude. bun install --frozen-lockfile completed before first migrations.
- Red: `go test -json ./internal/delivery ./internal/phases -run TestOwner`, /workspace/tmp/owner-red3.json: 9 expected failing leaf cases (4 waiver, 3 dispatch, 2 replacement); 3 no-work controls passed; no missing-DB skips. Earlier fixture-development runs red/red2 contained fixture/build errors, resolved before this red evidence.
- Green: `go test -json ./internal/delivery ./internal/phases -run 'TestOwner|TestARetrysNote|TestAResumedFixer'`, owner-green2.json: 23 test/subtest passes, zero failures/skips. Includes removed-secret retirement, production spec prompt for replacements, fresh-phase prompts from persisted notes and existing resumed-fixer directive tests.
- Guards so far: no SQL files changed, no added skips, no removed static test IDs; 3 regression test IDs added; diff --check passed.
- Pending: isolated mutations, clean targeted package tests, build/vet/typecheck, runtime collected-test-ID comparison.

## Commits
- 65ff460c5 plan and initial report.
- eada7af50 DB regressions (red).
