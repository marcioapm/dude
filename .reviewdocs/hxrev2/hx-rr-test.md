# Harnesses fix round 2 — test review

## Verdict: REQUEST CHANGES

Scope strictly `git diff da210bb4 c843683e`. Checkout HEAD is `25b0f4c8`; its only differences from `c843683e` are the supplied `.reviewdocs` reports. Reviewed application files match the requested endpoint. Read the fix report and all three first-round reports. No prompt, lux, proxy or settled role/tier design changes proposed.

## Medium findings

### M1 — tolerant stored-harness schema also accepts invalid new project writes

`packages/domain/src/hierarchy.ts:74` changes the shared field to `.catch(undefined)`. `apps/control-plane/src/api/routes/projects.ts:52` uses that schema for POST and, through `updateProjectInput`, PATCH (`:60-62`, `:215`). These are new writes, not tolerant reads. Unknown strings and wrong types are silently discarded; replacing `agentModels` can remove an existing project harness and change the effective adapter rather than rejecting the request.

**Executed repro**, real Router, migrated scratch Postgres, admin person principal:
- `POST /v1/projects {name:"RR",slug:"rr",agentModels:{reviewer:{harness:"aider"}}}` returns **201**, `agentModels:{reviewer:{}}`.
- `PATCH /v1/projects/:id {agentModels:{reviewer:{harness:42}}}` returns **200**, `agentModels:{reviewer:{}}`; the DB stores that cleared configuration.
- Domain `agentModelsSchema.safeParse({reviewer:{harness:"aider"}})` succeeds; `settingsPatchSchema` correctly rejects the same invalid harness.

Separate tolerant stored-value parsing from strict project create/update validation. Keep the existing unknown-stored-value test, but add invalid-new-write cases for project POST/PATCH and assert refusal and an unchanged row. The new `project-keys.test.ts` test proves only that an invalid request is accepted when echoing stored data; it does not distinguish stored tolerance from new input.

### M2 — old persisted Claude tasks lose their IDs after the format change

`orchestrator/internal/phases/translate.go:135-145` loads old `claudeTasks` unchanged, defaulting `claudeTaskNext` to zero. Before this diff, saved tasks contained only `content`/`status`, with the array index supplying their ID. New `claudePlan` looks up `task["id"]` (`translate_claude.go:282-285`) and allocates from `claudeTaskNext` (`:277-278`). An orchestrator deployment/restart during an existing Claude process therefore makes every old TaskUpdate miss and assigns ID 1 to the next newly created task, although Claude will call it task 3 (or higher).

**Executed, compiling overlay repro** `TestRRBackwardTaskState`: unmarshal
`{"claudeCost":0.1,"claudeCostSeen":0.1,"claudeTasks":[{"content":"old one","status":"pending"},{"content":"old two","status":"pending"}]}`;
then call `TaskUpdate {taskId:"2",status:"completed"}`. Result is **nil / no plan update**, not the two-task plan with task 2 completed. `TaskCreate` subsequently starts the counter at 1 instead of 3. Same-process restart is distinct from a new session epoch: clearing tasks on epoch 2 does not address this deployment case.

Backfill missing IDs from the old array's 1-based positions and recover the next-ID counter when loading legacy state. Add an executable old-state loading/update/create test; the current restart tests all create state in the new format.

## Fixes checked and sound within the tested cases

- **H1:** compared read-only with `~/git/lux/internal/adapter/claude.go:165-184`: `turn_end`, `maybeIdle`, `result`. Fake dialect uses exactly that order. Translator holds idle, persists the half/usage/idle, emits one turn accounting event, and reads cost/failure on the second half. Batch/restart, null result failure, and interrupted-turn tests pass.
- **N1:** epoch 1 `.10`, epoch 2 `.30` produces `.40` and resets task numbering. Task pruning preserves new-format IDs and updates. Legacy loading is M2 above.
- **Codex:** real recording totals are pinned to input **75264**, cacheRead **139520**, output **1846**; synthetic two-turn/reset/restart assertions pass.
- **Resume:** both new root integration tests pass and inspect actual `ResumeSecrets`, requiring `ANTHROPIC_API_KEY` and forbidding `DUDE_LLM_KEY`, after editing the role to OpenCode.
- **N2:** config overrides replace default keys; descendant/ancestor provider keys are rejected; sibling provider table keys remain usable. An additional executable Python `tomllib` parse of generated settings verifies sandbox override, dotted table ordering, booleans, mixed array values, sibling provider, and escaped DEL round-trip. Domain DEL rejection passes. Unsupported args are warned at `spec.go:674-676`.
- **Migration/image:** scoped diff retains the column default and NOT NULL, adds object CHECK NOT VALID, and only reorders the harness install layers; entire Codex vendor copy remains. Scratch migration succeeds; catalog confirms `convalidated=false`, and an attempted new run with `harness_state='[]'` is rejected specifically by `runs_harness_state_check`. No removed runtime install behavior identified. Image not rebuilt.

## Validation

Bun **1.4.2**, prescribed PATH. Own `hx-rr-test-pg`, pgvector PG17 on port **55982**, credentials from `dbtest.adminConn`. Go tests use a scratch overlay replacing the unrelated macOS-incompatible image-check file; production `containers.go` is untouched. No live model/auth access.

- Domain harness/tiers: **21 pass**, 0 fail.
- All selected checked-in phases tests: **pass**, including all three spec goldens; only the additional legacy task repro fails.
- Both resume integration tests: **pass** (3.155 s package total).
- Fake lux real-order test: **pass**.
- Generated TOML semantic parse: **pass**.
- Real project POST/PATCH strictness repro: **fails expected strict behavior**, as M1.

### Red-before and compiling mutations

All mutation results below are runtime assertion failures, not build failures. Scratch sources only; no tracked mutations.

| Check | Result |
|---|---|
| Old translator from `da210bb4`, new H1/N1 tests | Behavioral red: prematurely done, costs `[nil,.25]`, total `.25`; old task updated instead of new epoch task. A test-only `maxClaudeTasks=200` compatibility constant was needed to compile the newer tests; the initial missing-symbol attempt is not counted. |
| Remove idle hold | Killed: second turn done before result. |
| Do not restore `claudeHalf` | Killed: second turn not done, costs only `[.25]`. |
| Do not reset process costs | Killed: `[.1,.19999999999999998]`, total `.3` rather than `.4`. |
| Append override instead of replacing | Killed by existing override test; executable TOML probe also rejects duplicate sandbox key. |
| Remove submitted-harness assignment at `sync.go` only | Killed: real resume refused because `ANTHROPIC_API_KEY` is missing (22.662 s). |
| Remove submitted-harness assignment at `session.go` only | Killed: same real-secret refusal (22.601 s). |
| Remove provider nesting guard | Killed: evil provider descendant accepted and dropped list wrong. |

N2 historical red-before attempt with `da210bb4` spec does not compile because the newer test calls `codexSettings`, absent there. It is **not counted** as behavioral red or as a mutant. The compiling replacement/provider mutants above are the independently rerun N2 evidence; historical N2 red remains supported only by the supplied fix report.

## Brief Lows / coverage gaps

- Script and recording now put `turn_end` before `result`, but still omit the intervening lux idle. Their two-line order test does not prove the full real order. Fake lux and synthetic H1 feeds do exercise the full triple.
- New N2 tests assert generated string fragments, and the DEL test does not fail if CODEX_CONFIG is absent. They do not themselves parse TOML; the additional reviewer probe does. Preserve assertions and add parser/value checks rather than weakening them.
- Pruning test updates task 250 before restarting, then inspects persisted state; it does not create/update after loading pruned state. New-format counter persistence is visible in code but not independently mutation-pinned here.
- Reverse half order, fallback closing of an incomplete pair at a new epoch, and persisted synthetic assistant error before result are not independently exercised in this review. Do not infer coverage from comments.
- No full root suite, browser E2E, live CLI/model, or image build run. Original fix report's broader gates are not presented as rerun results.

## Cleanup

All background checks completed. Removed only the owned `hx-rr-test-pg` container. `git status --short` is empty; both working-tree and staged diffs are empty. Report/scratch artifacts are ignored. No commit, push, stash, tracked mutation, production image-check edit, live model call, or auth-file access.
