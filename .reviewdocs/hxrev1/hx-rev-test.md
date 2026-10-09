# Review (test quality): harnesses `e5c52d25..58d91b02`

## Bottom line
**REQUEST CHANGES.** The test suite is mostly strong. Spec, plan, tool-naming, misfit and failed-turn tests kill 17 of the 23 mutations tried (counting call-site variants). Four findings block approval:

- **H1:** the Claude Code translator is tested in an event order lux never sends. Under lux's real order, the cost of each turn lags one turn behind, the last turn's cost is lost, and a failure seen only in `result` fails the next turn instead of its own. Reproduced.
- **H2:** `TestEachHarnessesSpecIsTheGoldenOne` FAILS at head. The merge with #89 (`app=dude`) did not regenerate the three harness goldens.
- **M1:** Codex token accounting is not pinned: 4 of 4 mutations survive.
- **M2:** "a resume keeps its harness" is tested only on the helper; removing either call site survives.

Setup: an overlay stubbed `syscall.Getxattr` in a scratch copy of `internal/images/containers.go`, so `./internal/phases` builds on darwin. A throwaway pgvector/pg18 container served `DUDE_TEST_PG`. No tracked file was left changed; `git status` is clean.

## Findings

### H1 (High): the Claude translator assumes `result` comes before `turn_end`; lux sends `turn_end` first
- lux `internal/adapter/claude.go:165-184`: on a `result` line it calls `sink.Event("claude.turn_end", …)` at :174, then falls through to `sink.Event("claude."+m.Type, line)` at :184. So **`claude.turn_end` comes before `claude.result`**. Codex is fine: `codex.turn_end` comes before `codex.turn/completed`, and the translator reads nothing from the latter.
- `scripts/real_harnesses.py:162-167` appends `claude.result` and then synthesises `claude.turn_end`, the reverse of lux. Two places carry the wrong order: the "real" recording `testdata/claude-code-real.jsonl` (lines 276/277) and `TestClaudeCodesTasksAndCostOutliveARestart` / `TestAFailedTurnOnEitherHarnessFailsTheRun`, which feed `result, end`. The fake lux does the same (`fakelux/dialect.go:88-89`), so the root E2E is blind to it too.
- `translate_claude.go:56-63` stores `total_cost_usd` and `turnError` on `result`. `claudeTurnEnd` (:288-320) reads them. In lux order they always belong to the previous turn.
- Repro: `.taskdocs/overlay/repro_order_test.go`, run through `-overlay .taskdocs/overlay2.json`:
  - The real recording with its last two lines swapped into lux order: the turn end has **no `costUsd`** (`TestReproLuxOrderRealRecording` FAIL). `runs.agent_cost_usd` stays 0 until lux prices the Run.
  - Two turns at 0.25 then 0.75 total: costs are `[<nil> 0.25]`, want `[0.25 0.5]`.
  - `result{is_error:true}` with no `error` assistant line (API 500, `error_during_execution`): the Run stays **running**. The next, successful turn then **fails** it with the stale reason (`TestReproLuxOrderStaleErrorFailsTheNextTurn`: "after the failed turn: running; after the next, successful turn: failed").
- Not affected: a failure Claude Code reports as a synthetic assistant line with `error`. That line comes before both events, which is the case the test's model_not_found example covers. Tokens are also fine, because `turn_end` carries `usage` itself.
- Test-quality point: the recording is presented as "lux would send dude" but was produced by a script that re-implements lux's adapter incorrectly. No test asserts the order against lux. Fix both: read cost and error from whichever of the pair arrives second (or from `turn_end` plus a deferred `result`), and correct the script, the recording, the fake lux and the unit feeds to lux's order.

### H2 (High): a harness golden test fails at head
`go test -run TestEachHarnessesSpecIsTheGoldenOne ./internal/phases` fails at `58d91b02` for all of opencode, claude-code and codex. Since #89, `buildSpec` adds the label `"app": "dude"`. The `spec-harness-*.golden` files were recorded before the merge, so they lack 12 lines. Running with `-update` adds exactly those 12 `"app": "dude"` lines and nothing else (checked, then reverted). The report's green gates are from `f6f08304`, before the merge. In the full `./internal/phases` run, this is the only failure. To get a meaningful mutation baseline, I regenerated the goldens temporarily and restored them afterwards.

### M1 (Medium): Codex token summing is unpinned
`TestARealCodexTurnIsTranslated` (`translate_harness_test.go:298-301`) asserts only that `input`, `output` and `cacheRead` are each > 0. No other unit test drives `codex.thread/tokenUsage/updated`. The root E2E checks `output == 34` with a single usage report per turn, so it cannot distinguish summing from taking the last report. These mutations all **survived** `./internal/phases`:
- an off-by-one: each report adds the previous request's tokens, so the turn's last request is dropped (the requested mutation);
- only the last request counted (`=` instead of `+=`);
- `t.codexTurn` not reset at turn end, so tokens leak into the next turn;
- `input` not reduced by cached input (double-counts cached tokens).

The recording has 14 reports. Their exact sums are known (input 214784, of which 139520 cached; output 1846), so one exact assertion (`input 75264, cacheRead 139520, output 1846`) would kill all four. A two-turn synthetic feed would kill the reset mutation.

### M2 (Medium): resume-keeps-harness is tested only on the helper
`TestAResumeKeepsTheHarnessItWasSubmittedOn` calls `submittedHarness(...)` directly. Removing its body is **killed**. Removing the call at `sync.go:1000` (`settings.Harness = submittedHarness(ranOn, …)`) **survives** `./internal/phases`. So does removing the call at `session.go:52`. `orchestrator/harnesses_test.go` has no resume test (read: it covers delivery, ask, steer, misfit and a real model). No test changes a role's harness between submit and resume and asserts that the resumed spec keeps the old adapter. The property the brief settles ("resumes keep the submitted harness") rests on two unprotected lines.

## Mutations (unit packages `./internal/phases ./internal/delivery`, golden regenerated for the baseline)
| Mutation | Result | Killed by |
|---|---|---|
| drop `--thinking-display summarized` | killed | golden only |
| key in env, not a secret (Claude) | killed | golden, TheKeyIs… |
| key in env, not a secret (Codex) | killed | golden, TheKeyIs… |
| `ANTHROPIC_BASE_URL` keeps `/v1` | killed | golden only (`TestClaudeCodeIsGivenTheProxysURLWithoutV1` tests the helper, not its use) |
| no `model_supports_reasoning_summaries` | killed | golden only |
| Codex `config.toml` not written | killed | golden only |
| Claude cost: running total, not the delta | killed | TasksAndCostOutliveARestart |
| Codex tokens off by one turn/request | **survived** | — (M1) |
| `mcp__dude__` not stripped | killed | RealClaudeCode…, ToolsAreNamed… |
| unknown tool dropped | killed | ToolsAreNamed… |
| `TaskUpdate` not mapped to the plan | killed | RealClaudeCode…, TasksAndCost… |
| failed turn does not fail the Run (Claude / Codex) | killed / killed | AFailedTurn… |
| misfit inverted (Claude / Codex) | killed / killed | HarnessThatCannotRun…, golden |
| fixer does not follow the implementer's harness | killed | ARolesHarnessIsLayered… (delivery) |
| resume takes the role's harness: helper | killed | AResumeKeeps… |
| resume takes the role's harness: `sync.go` call | **survived** | — (M2) |
| resume takes the role's harness: `session.go` call | **survived** | — (M2) |
| extras: Codex no reset / last-only / input incl. cached | **survived** | — (M1) |

Six spec mutations are killed only by the byte golden. That is acceptable, since the golden is reviewed text, but it is also why H2 matters: a stale golden that everyone regenerates blindly would let all six through.

## Red before (production files from `e5c52d25`, tests and testdata kept)
- Go: all 25 new tests fail to compile on main (`undefined: delivery.HarnessOpenCode`, `HarnessName`, …). That counts as red, though it is not a behavioural red.
- Bun: domain `harnesses.test.ts` (3 tests) fails (module missing). runScreen "and the harness that ran it, first…" fails, as do the two changed chip tests. The settings.test.ts `roleChanged` harness line fails. 8 migrate tests fail (101 is missing).
- **Passes on main:** runScreen "the scripted agent names no harness". It asserts `"Coder ·fake/scripted"`, which main already renders. It guards against a future regression, but it is not red-before.
- Head: all of the above pass (domain/web 39 pass; migrate 15 pass). Go: everything passes except H2.

## Recorded testdata
- Claude recording: real `thinking` (5, with text), `text` (4), `Read` ×2, `Bash` ×3, `Write`, `Edit`, `mcp__dude__emit_event`, `TaskCreate`/`TaskUpdate` ×8 each, one result/turn_end. It covers thought, message, read/bash/write/edit, plan, MCP and turn end, except for the order defect (H1). `TodoWrite`, Grep/Glob, `mcp__github__*` and an unknown tool are synthesised only. That is acceptable: the report says 2.1.207 does not offer them, and the shapes match.
- Codex recording: `reasoning` ×2 (summaries of 393 and 578 chars), `agentMessage` ×3, `commandExecution` ×5, `mcpToolCall dude/emit_event`, plan ×7, 14 token reports, turn_end. **`fileChange` is synthesised only**, as are failed command/MCP and `codex.error`. Acceptable given report finding 2 (no `apply_patch` under app-server for proxy model names): the shape follows the app-server schema. It should stay flagged as unverified.
- Secrets: grepped for `sk-`, `Bearer` and `key`. Only the fixture placeholders `sk-golden` / `Bearer tok_golden` in the goldens, `"apiKeySource": "ANTHROPIC_API_KEY"` (a name), and SQL/prose uses of "key". Nothing secret.

## E2E (read; the root package cannot build here)
- `harnesses_test.go::TestAScriptedDeliveryOnEachHarness` runs the fake lux's Claude/Codex dialect through the **real translator** inside the real orchestrator. It asserts the thought text, the message text, a `read` call with its completion, one plan, 2 progress events via dude's tools, and the turn end with output 34. This is translation-level proof, not just "the fake emitted". Its dialect shares H1's wrong Claude order and has one usage report per turn (hence M1).
- `test_web_ui.py::…delivers_and_its_chat_shows_its_work[claude-code|codex]` checks in the browser: the harness chip, the message "Implemented it.", a Thought button with the scripted thought, and a `[data-tool='read']` card. This proves thought, message and tool card for each harness through the deployed translator and UI.
- `test_settings.py` covers layering, follows-implementer, reset as a delete, refusal of `aider`, and the inline misfit callout and its clearing. Behavioural throughout.
- No assertions on SQL text or source code. The SQL in tests queries fixtures for rows. Golden comparison of the lux spec is a byte golden over the built value, not source inspection.

## Test IDs
- Go (`git grep '^func Test'`): 1099 → 1124. **25 added, 0 removed.** The list matches the report.
- Bun (diff of `test(`/`describe(` lines): added runScreen ×2, domain harnesses ×3 plus one describe. **None removed.** The changed expectations are in 3 chip tests and migrate lists.

## Lows
- `TestPrintAHarnessSpec` and `TestPrintATranslation` are script hooks registered as tests that always skip in CI. They inflate the test list; consider a `//go:build` tag or a `cmd/`.
- `TestClaudeCodeIsGivenTheProxysURLWithoutV1` tests `anthropicBaseURL` only. Its use in the spec is pinned by the golden alone.
- `TestARealClaudeCodeTurnIsTranslated` asserts `cost == runs.agent_cost_usd`. Under H1 that relationship holds only because the order is wrong.
- The recordings embed `/Users/marcio/...` paths (20 occurrences). Harmless, but personal.
- `harnessMisfit` treats any non-`claude*` model as OpenAI (`modelProvider("my-claude") == "openai"`). That is tested and intended, so it is just a note.
