# hx-rev-qual: code quality and correctness review of `e5c52d25..58d91b02`

## Bottom line

**REQUEST CHANGES.** There are two new Medium findings: N1 and N2. I also confirm the test reviewer's H1.

The spec construction, the secret handling, the state-volume coverage, the fit check, the settings and UI plumbing, and the image are all sound.

- **N1:** the cost delta breaks across a resume.
- **N2:** `options.args` never takes effect on Codex.

Both are small fixes.

## Findings

### N1 (Medium): a resumed Claude Code process under-counts its first turns' cost

**Where:** `translate_claude.go:296-300`.

**The logic:** cost = `claudeCost - claudeCostSeen`. The whole value is used only when the total *drops* (`claudeCost < claudeCostSeen`). The report (finding 4) confirms that a `--resume` starts `total_cost_usd` again from 0.

**The failure:** if the new process's first total is already ≥ the old process's last total, nothing tells the translator a new process began. The previous process's total is then subtracted from the new one. This is the normal case for a parked Run (the old total was small, the resumed turn is big).

**Reproduced:** I wrote a temporary test, since removed. It ran the phases test binary, built for linux/arm64, against a migrated Postgres. It feeds `result(0.10)` + `turn_end` at epoch 1, then establishes session epoch 2 (a resume) and feeds `result(0.30)` + `turn_end` at epoch 2:

```
turn costs [0.1 0.19999999999999998], run total 0.3 (want 0.10, 0.30; 0.40)
--- FAIL
```

The resumed turn's $0.10 is lost. Each later turn in the new process stays correct, because the delta then runs against the new process's own totals, so the loss is exactly one previous-process total per resume.

**Why it matters:** `agent_cost_usd` is shown until lux's cost plugins report. It is also the permanent figure wherever lux never prices the Run (`run_metrics`, migration 061).

**What the existing test covers:** `TestClaudeCodesTasksAndCostOutliveARestart` covers only the "total went down" case (0.75 → 0.1).

**Fix:** key the running total to the process.
- Reset `claudeCostSeen` to 0 when a new session epoch is established. `translator.session`, `!first` branch, `translate.go:667`, already knows about the resume.
- Or store the epoch alongside `claudeCostSeen` in `harness_state` and treat a different epoch as a new process.

**Interaction with H1:** this compounds with H1. Once H1 is fixed by reading cost in `result` (or by handling `turn_end` lazily), the per-process reset still has to happen.

### N2 (Medium): a tier's `options.args` are silently dropped on Codex

**Where:** `spec.go:505` appends `HarnessArgs(in.Options)` to `Workload.Command`. lux's codex adapter then appends `app-server -c mcp_servers.dude.url=…` (lux `internal/adapter/codex.go:92-103`). Every dude Run has the dude MCP server (`spec.go:352-355`).

So the tier's args always land **before** `app-server` with a `-c` after it. The report's own finding 1 shows Codex 0.144 drops every pre-subcommand override in exactly that position. The args are not written into `config.toml`, so nothing recovers them.

**Reproduced** with the local `codex-cli 0.144.1`. A throwaway `CODEX_HOME` holds a config.toml naming model `gpt-6-sol`. The `thread/start` result shows:

| invocation | `reasoningEffort` | `model` |
|---|---|---|
| `codex -c model_reasoning_effort="low" app-server` | `low` | |
| `codex -c model_reasoning_effort="low" app-server -c mcp_servers.dude.url=…` | `None` (dropped) | |
| `codex -m other-model app-server -c mcp_servers.dude.url=…` | | `gpt-6-sol` (`-m` ignored too) |

**Why it matters:** `docs/operations.md` ("only `{"args": ["…"]}` is used, appended to the command") and `docs/design/model-tiers.md` ("extra command-line arguments appended as given") both promise that args work. On Codex they have no effect. Nothing is logged, and no test catches it: the golden only shows that the args are in the command.

**Claude Code is unaffected:** its adapter appends only plain flags (`-p --input-format …`).

**Fix, any one of:**
- Turn `-c key=value` pairs from args into lines of `config.toml`.
- Reject or log args on Codex.
- Document that args on Codex take effect only once lux puts its overrides before the subcommand.

### Confirmed from the test reviewer

**H1 (confirmed and extended).** lux sends `sink.Event("claude.turn_end", …)` inside the `case "result"` branch (lux `claude.go:174`), then `claude.result` after the switch (`:184`). `claudeTurnEnd` therefore reads the `claudeCost` and `turnError` of the previous `result`.

The recorded file `claude-code-real.jsonl` has them in the right order (result at line 275, turn_end at 276) only because `scripts/real_harnesses.py:162-167` writes them in the opposite order to lux. So the "real" recording is not lux's order for this pair, and `TestARealClaudeCodeTurnIsTranslated` cannot catch H1.

The fake lux dialect (`fakelux/dialect.go:88-89`) makes the same inversion, so the E2E suites cannot catch it either.

What I saw locally with `claude` 2.1.207 (stream-json, endpoint unreachable, then an interrupt): the only error signal was `result{subtype:"error_during_execution", is_error:true, result:null}`. No assistant line had `error` set. Because `result` is null, `line.IsError && line.Result != ""` (`translate_claude.go:61`) does not fire. That turn ends with no failure recorded, which may be fine for an interrupt. A non-interrupt `error_max_turns` or `error_during_execution` would pass silently in the same way.

M1 and M2 I did not re-check beyond what is noted below.

## Lows

- **Subagent lines are treated as the main agent's.** `translate_claude.go` ignores `parent_tool_use_id`. If Claude Code runs a `Task`/`Agent` subagent, its assistant and user lines are recorded as the main agent's messages and tool calls, and its `usage` overwrites `t.usage.context`. The recording contains no subagent lines, so this is untested.
- **`tomlString` and DEL.** `spec.go:519` relies on JSON ≈ TOML. A header value containing DEL (0x7f) passes `tierHeadersSchema`, which forbids only NUL, CR and LF. JSON leaves DEL raw and TOML forbids it raw, so the CODEX_CONFIG file fails to parse and Codex will not start. Other control characters are escaped by JSON as `\uXXXX`, which is valid TOML.
- **Session creation accepts any harness string.** `apps/control-plane/src/api/routes/work.ts:377`: `createSessionInput.harness` is still `z.string().min(1)`, so an admin can create a session row with any harness string. This predates the branch, but it now disagrees with the enum.
- **Finding 11 (enum parse).** The read paths do not parse stored `agent_models` with `agentModelsSchema`. The settings, models, machines and work routes cast the JSON (`as AgentModels`). `resolveHarness` and Go's `ResolveRole` both skip unknown values, and `RunTierChip` hides an unknown `runs.harness`.

  The one write path is a full-replace `PATCH /v1/projects/:id {agentModels}` (`projects.ts:52`), which parses the whole object. A project that already stores e.g. `harness: "aider"` would get a 400 from the project editor until it is cleaned. No migration, seed or fixture writes a harness into `agent_models` (the E2E `test_project_harness_preference_is_honoured` reads `runs.harness`/sessions only).

  A tolerant parse (`harnessSchema.catch(undefined)`) or a one-line migration nulling unknown values would close it. The risk is low.
- **Chip has no harness for scripted sessions.** The chip prefix shows "OpenCode ·" for every recorded real harness. The only `TierChip` outside RunScreen is the design-system gallery, which shows none, so it is consistent. Scripted Runs record `dude.harness = claude-code/codex` (`spec.go:374-376`) and so get a chip prefix. Scripted OpenCode Runs record `scripted` and get none, which matches the report's finding 8.
- **No error for a misfit on resume.** `harnessMisfit` is skipped on resume (`sync.go:1000`, `session.go:53`). That is intended (finding 9), but a resumed Run on a harness whose tier was since changed to a misfit model gets no error. That is fine, because the stored model is reused.
- **Docs:** `docs/operations.md` describes `options.args` as working on both harnesses (see N2).

## What I checked and found sound

- **Claude command and lux's additions.** dude's command plus `-p --input-format stream-json --output-format stream-json --verbose [--resume id] [--mcp-config …]` is valid. `--thinking-display summarized`, `--thinking disabled` and `--effort max` were all accepted by `claude` 2.1.207, and a bogus value is rejected. Effort values `low|medium|high|max` are within Claude's accepted set.
- **`ANTHROPIC_BASE_URL`** has `/v1` (and a trailing `/`) trimmed. `ANTHROPIC_CUSTOM_HEADERS` is sorted `Name: value` lines. Values cannot contain CR or LF (`tierHeadersSchema`).
- **Codex `config.toml`.** It is the same settings joined with `\n`, and values are quoted via `tomlString`. I probed it on 0.144.1: `model`, `model_provider=dude`, `reasoningEffort` (`max` and `xhigh` are accepted by Codex itself) are all applied from the file. It does not clash with lux's `-c mcp_servers.dude.*` (different keys; a `-c` overrides the file per key).
- **Codex file secret on resume.** It is resupplied, because `resume()` passes the fresh `spec.Secrets` (`sync.go:1875`). It is placed as a symlink to the secrets tmpfs (lux `shim.go:733-746`), so the value is never in a state-volume snapshot.
- **Key placement.** It exists only as `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` (env secret). `DUDE_LLM_KEY` is absent on those harnesses. A grep of the goldens and testdata for key-shaped strings found nothing; `apiKeySource: "ANTHROPIC_API_KEY"` is a name, not a value.
- **State volume.** The home volume `/home/agent` (state) covers lux's `StatePaths` `$HOME/.claude` and `$HOME/.codex` (lux `spec.go:330-339`, `:622-631`).
- **Codex translator against the recording.**
  - Token sums over `last` reproduce `total` exactly: 214784/139520/1846.
  - `input` excludes cached input.
  - `turn_end` precedes `turn/completed` in lux (`codex.go:375`), and the translator reads only `turn_end`, so there is no ordering issue on Codex.
  - `error` with `willRetry:false` is kept; a failed status fails the Run.
- **Tool names.** `mcp__dude__x` maps to `x`, other servers to `server_tool`, and unknown tools pass through (Claude). Codex `mcpToolCall` uses the same rule. `isEdit` covers `edit`/`write`/`multiedit`.
- **`harness_state`.** It is loaded with the cursor and saved in the same transaction (`translate.go:207-221`). `codexTurn` is cleared at turn end. Claude tasks persist across turns by design. `turnError` is not persisted; a restart between an error line and its `turn_end` would lose it (Low; the same holds for ACP buffers).
- **Settings.** `resolveHarness` (TS) matches `ResolveRole` (Go): project → org, fixer → implementer, unknown values skipped, default opencode. The misfit rule is identical (a `claude-` prefix, and `fake/` exempt). The settings GET returns `harness` and PATCH accepts `harness|null`. The UI field follows the Machine/Image pattern.
- **Image.** Pinned `CLAUDE_CODE_VERSION=2.1.207` and `CODEX_VERSION=0.144.1`, `bwrap` symlinked on PATH, `DISABLE_AUTOUPDATER` and `check_for_update_on_startup=false` set.
- **Tests run:** `TestARealClaudeCodeTurnIsTranslated`, `TestARealCodexTurnIsTranslated`, `TestClaudeCodesTasksAndCostOutliveARestart` and `TestAFailedTurnOnEitherHarnessFailsTheRun` all pass. They ran in linux containers against a disposable Postgres. Both have been removed.

The working tree is clean (`git status` shows nothing tracked). The only untracked items are the ignored `.taskdocs/` and `node_modules/` from `bun install`.
