# Preview Create / Start Findings

## Scope and Baseline

- Started from clean detached main commit `7ae795984`; branch `marcio/preview-create-start`.
- No AGENTS.md found. Read docs/CONTRIBUTING.md, docs/cli.md, CLI preview code, server routes/service/wake code, and branch preview E2E suite.
- Scope: CLI composition, API route manifest, docs, focused and subprocess tests. No release, deploy, push, PR, or merge.

## API Findings

- POST project branch-previews creates/reuses; GET branch-preview has URLs but no process readiness.
- GET run servers returns `{run, servers, recipes, moved, preview}`. Recipes have `autostartInPreviews`; servers have live process `state`, `epoch`, and wakeable `serverState`.
- Per-server POST start takes an empty body and uses the existing authenticated public API. Starting an asleep preview requests its existing wake lifecycle.
- Added RunView.epoch from live lux Get; readiness rejects a mismatching server epoch, wrong run attachment, asleep/waking serverState, and stale process-ready values.
- Existing previewURLs silently returns stale success on HTTP errors and sleeps beyond its deadline; replace it.
- Existing fakeLux naturally advances servers to ready (tests/suites/test_servers.py); retain the original API branch lifecycle test unchanged.

## Planned Verification

- Focused httptest CLI coverage, full CLI package tests, route/body control-plane gates.
- Durable CLI subprocess E2E via tests/run_tests.py against isolated Postgres, CP/orchestrator and fakeLux.
- Safe committed-tree regression mutants for create waking, start not waking, and premature wait success.
- Record exact results/counts and limitations here as work proceeds.

## Implementation / Gates So Far

- CLI composes existing per-recipe start actions. Allocation is bounded at 30 seconds; a positive wait bounds the entire command and all HTTP calls. Nonwait requests all expected starts and prints a snapshot without claiming ready.
- Multi-server subprocess coverage exposed a real race: another start can move paused to scheduled before a later start reaches wantWake. CLI retries only the existing explicit transient 409 not_running / no-longer-asleep responses, bounded by the same deadline. Other start/poll errors fail.
- Full CLI package and CLI vet passed. Control-plane route/body tests: 14 passed after frozen-lockfile dependency installation and mapping empty start bodies.
- Initial Postgres image lacked vector, causing migration failure in E2E and servers tests; switched to documented pgvector/pgvector:pg17-trixie in isolated tmpfs container on 55434. No product workaround.
- internal/servers package passed (5.738s). Final committed-tree API+CLI E2E passed: 2 tests in 5.23s, including two-server and never-ready cases (`e2e-committed.log`).
- Original API lifecycle function preserved byte-for-byte. Existing CLI lifecycle test retained, strengthened to create asleep, both start forms, both stop forms, create --start --wait, and one-ready/one-starting timeout.

## Commits

- `03b303259` Expose the live server Run placement epoch for readiness checks.
- `4f966f6b5` Separate preview create from start and wait for live server readiness.
- `dfc82e5ee` Exercise preview create, wake and readiness through CLI subprocesses.
- `ebd73b092` Assert invalid preview starts never create or mutate previews.
- All commits use owner-requested personal identity. No push or PR.

## Final Gate Counts / Test Preservation

- Full CLI: 142 passing test/subtest events, 87 top-level tests; zero skips/failures. Baseline main: 114 events, 86 top-level. One renamed test (`TestPreviewStartPrintsItsURLs` to `TestPreviewCreatePrintsItsURLs`); one new lifecycle test with 27 subtests. No other old test removed.
- CLI race: passed (2.388s); focused lifecycle repeated 10 times: passed. CLI vet and git diff --check: passed.
- Server-service package: 67 passing test/subtest events, 41 top-level; zero skips/failures. Existing memory test now also checks current placement epoch.
- Control-plane CLI route/body gates: 14 passed, zero failures. All existing test names retained; request golden renamed to create and augmented with empty-body real start.
- Branch E2E: same two test names before/after; original API test byte-for-byte unchanged. Original CLI idempotency, naming, list/show, stop ID and stop project+branch checks preserved. URL polling coverage moved to explicit asynchronous-allocation unit scenario.
- Route cases grew by two (two former create-like start rows replaced by create, create --start, start project+branch, start ID). Manifest checks all composed route/command pairs.
- Gate logs stay local under `.taskdocs/*.log`; never staged. Dependencies installed using locked manifests without changes to lockfiles.

## Safe Regression Mutants

All mutations were confined to a detached worktree created from committed `dfc82e5ee` under `/tmp/opencode/dude-preview-mutants`; primary dirty tree was never mutated.

- Create accidentally wakes (`wake := true`): killed by focused create-asleep test, which observed two unwanted start POSTs. Also killed by actual CLI subprocess lifecycle: never asleep (`mutant-create-e2e.log`, 61.02s).
- Start does not wake (omit start POST): killed by focused start-ID test, which observed zero wake POSTs. Also killed by actual CLI subprocess lifecycle: both servers remained stopped/asleep and wait timed out (`mutant-start-e2e.log`, 30.93s).
- Wait returns on URL allocation/running (replace ready completion predicate with allocated): killed by focused running-not-ready deadline test. Also killed by actual CLI subprocess readiness assertion (`mutant-wait-e2e.log`, 1.03s).

## Limits / Deliberate Non-Goals

- No full Go root gate, live production smoke, UI, release, deploy, push, PR or merge performed.
- Epoch is additive and based on a live lux Get; existing server API degrades unavailable lux reads to empty servers. CLI therefore fails closed (missing/current-epoch mismatch or timeout), not ready on missing data. Direct public API poll errors fail immediately.
- Server process `unreachable` remains non-ready and times out with live state; process `exited`, server `no answer`, run startup errors and terminal preview/run states fail immediately.
- Empty expected recipes fail clearly; missing allocations never silently no-op. Create-only URL allocation retains its separate 30-second bound. Positive wait covers the whole command, not an extra readiness period after allocation.
- Transient wake race retry uses only explicit existing API refusals and introduces no backend parallel lifecycle or batch endpoint.
