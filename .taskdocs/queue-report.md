# Queued base sync report

## Result
Bug 2 implementation and focused proof committed. Validation is **not entirely green**: the phases package has four setup-script failures, reproduced unchanged at the supplied base. No PR/push, no delegation. Bug 1 person_note implementation/tests unchanged.

## Commits
- `56562e4ea` Queue explicit base integration independently of delivery snapshots
- `3ae9124da` Test queued writer routing and public-head ancestry completion
- This report/validation evidence is committed in a subsequent reporting commit (see git log).
Base: `65fb4d685475f92f9919b043abe72927ee9d1691`.

## Storage and behavior
Migration 220 adds tenant-isolated `base_sync_requests`, primary key `(pull_request_id, base_sha)`. Runtime claim/advance replaces non-owned workflow state and consumes claimed signals at transition commit. Independent rows avoid that overwrite and remain pending after directive delivery. Signals are wakeups only, keyed by requested base rather than PR head (dude#327).

Owner authorization precedes forge reads and is repeated in the transaction. Busy delivery accepts requests without launching another phase; existing awaitPR and conflict escalation routes remain. Halted/ended deliveries are refused. A live writer with BaseSync receives one directive per request/writer; otherwise capable implement/fix/simplify initial prompts include pending work. Reviewer/QA prompts are unchanged. Instructions require sync_base, merge lux/base, affected tests, commit, no rebase; no container git credentials or promotion bypass added.

Completion requires forge comparison: zero behind AND merge-base SHA equals requested SHA at the public PR head. Forge compare failures retain pending rows. UI exposes queued/resolving/complete from durable rows; ended writers no longer advertise resolving. Activity names a live writer and explicitly distinguishes candidate work from public PR publication.

## Red/green and gates
Full output remains local in `.taskdocs/queue-*.log`; logs are ignored by git and some browser failure diagnostics contain ephemeral fixture API keys, so those logs were deliberately not force-added.

- Frozen Bun install before migrations: passed on both hosts.
- Real pgvector Postgres 17 container, dude/dude on localhost:5433. No missing-database skips.
- Red: `queue-red.log`: new busy candidate-review test failed with HTTP 409 already-working refusal before implementation.
- Green: `queue-green.log`: same regression passed.
- Final focused root Go: `queue-final-counts.log`: 12 passing tests/subtests, zero skips/failures, including busy writer directive dedupe, reviewer-only queued acceptance, same-base idempotency, newer base same PR head, durable request under state writes, unintegrated retention, trusted public-head ancestry completion, owner/refusal/awaitPR/escalation compatibility.
- Go delivery package passed (139.817s); API passed (14.675s).
- Go phases **failed**: four setup/init tests, `queue-go-packages.log`. All four reproduced on pristine base worktree: `queue-base-phases.log`. Not fixed as unrelated.
- Vet delivery/API/phases/prs passed: `queue-final-vet.exit` = 0.
- Touched Bun suites: 40 passed, zero failed, 155 assertions, `queue-bun-final.log`. Existing nested-anchor React warning remains.
- Workspace typecheck passed: `queue-typecheck-final.log`.
- Python browser e2e: 1 passed, 3 deselected, `queue-e2e-retry.log` (23.77s). Earlier attempts passed queued API/UI assertions but failed next-writer inspection; corrected fixture to resume awaitPullRequest normally and inspect the scripted integration's original prompt label rather than its generated workload script.
- Pinned Python dependencies via uv frozen lock; installed Playwright Chromium v1243; explicit executable `/workspace/tmp/playwright/chromium-1243/chrome-linux-arm64/chrome`, no system Chrome assumption or silent browser skip.
- `git diff --check` passed. Diff guard captured in `queue-diff-guard.log`; owner-notes guard is empty. No test removals: Python collection 3 -> 4 IDs (one added); delivery/API/phases Go collected IDs preserved (timing-only diff). Root adds two tests and updates expected repeated-click behavior.
- Full root suite not run: reserved for dedicated validation rather than starting the broad suite with insufficient remaining validation budget.

## Remaining issues / coverage limitations
- Overall phases gate remains red for reproducible baseline failures.
- In-flight overwrite proof changes workflow state while a request is pending, but does not pause an actual runtime transition with a concurrent barrier. Independent-table storage makes workflow replacement unable to overwrite it; a stronger concurrency regression remains desirable.
- Forge outage retention is implemented but lacks a dedicated outage regression.
- Next-writer instructions are proven in the real-stack browser e2e; that fixture deliberately holds/resumes the workflow via SQL to deterministically model reviewer-only busy delivery.
- Completion is intentionally conservative: it tracks public PR ancestry, so a candidate that has integrated the base stays pending until normal gated publication.
