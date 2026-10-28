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
- Mutations (isolated, restored before broad gates): mutation-park.json failed all 4 waiver cases; mutation-restart.json failed all 3 replacement cases including retireForSecrets; mutation-wait.json failed all 3 work-dispatch cases and passed all 3 no-work controls. Each mutation process exited 1, no missing-DB skips. `git diff --exit-code` confirmed restoration before clean targeted gates.
- Clean targeted: `go test -count=1 -json ./internal/delivery ./internal/phases ./internal/api`, owner-targeted.json: 1,791 test/subtest passes, 4 failures, 3 optional real-harness skips, zero missing-DB skips. Delivery and API passed. Phases failed only TestARunsInitWritesItsInstructionsThenRunsItsSetup, TestASetupScriptRunsBeforeTheWorkloadAndOnARepositoryAResumeAdds, TestAFailingSetupScriptFailsTheRunBeforeItsWorkload, TestAHookASetupScriptInstallsRunsOnALaterCommit. All four reproduce at pinned base (owner-base-setup.json); isolated setup retry also failed. No out-of-scope setup fix attempted.
- `go build ./...`, `go vet ./...`, `bun run typecheck`: passed (owner-build/owner-vet/owner-typecheck logs and exit files).
- Runtime collected-test-ID diff: `go test -list . ./internal/delivery ./internal/phases ./internal/api` at base and HEAD, 949 -> 952 IDs, only the 3 new regressions added, none removed; both collection commands exited 0.
- Full root gate `scripts/go-test.sh` (detached, RUNNER_TEMP/TMPDIR/GOTMPDIR/GOCACHE under /workspace/tmp), owner-full-gate.log: exit 1. Root shards had 1,474 top-level results and no failures; there were no missing-DB skips. The others process failed 15 top-level tests across 4 packages: automations (4), fakelux (3), images (1), phases (the same 4 setup tests). The full log contains 5,479 PASS, 13 SKIP and 31 FAIL lines, counting subtests. The 11 non-phases failures were re-run at the pinned base (owner-base-other-failures.json) and failed there too, with the same readiness/podman/workspace symptoms. The phases setup failures also reproduce at base. Owner-note gates are green, but the full root gate is not green.
- Final regression run after the one-shot downstream assertion: owner-final-regression.json, 27 test/subtest passes, exit 0, zero skips. The final import grouping in rounds_keep_test.go is format-only and was re-verified in the final focused run below (`owner-final-head.json`: 27 test/subtest passes, exit 0, zero skips; package vet passed). Note: the full root gate began before the 643efd71f test assertion and the import regrouping, so it may not cover those last test-only edits.

## Commits
- 65ff460c5 plan and initial report.
- eada7af50 DB regressions (red).
- 0f4add780 owner-note fixes.
- 643efd71f mutation evidence and later-Run no-repeat assertion.
- Final report/import-format commit: this commit.
