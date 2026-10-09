# dude: Claude Code and Codex as harnesses, chosen per agent role (report)

Branch `harnesses`, pushed to `github` at `f6f08304`. No PR opened.
Base: `3f1daa07` (#83). `github/main` has since moved two commits ahead (#76
"Can run containers", #89 "app=dude"); this branch is not rebased on them.

## Commits, by section

| Section | Commit | What |
| --- | --- | --- |
| §2 spec | `dc316a2f` | `delivery.RoleSettings.Harness` (+ `HarnessName`, `HarnessFits`), resolved over the same layers as the tier; `buildSpec` takes the model part from `openCodeWorkload` (unchanged), `claudeCodeWorkload`, `codexWorkload`; fit check at build time (`harnessMisfit`) in `sync.go` and `session.go`; resumes keep the submitted harness (`submittedHarness`); `options.args` passed as CLI args, other keys logged (`logIgnoredOptions`); scripted `fake/*` on Claude Code/Codex uses that adapter with `lux-fake`. Goldens per harness. |
| §4 translator | `146b4779` | `translate_claude.go`, `translate_codex.go`, dispatch in `translate.go`; migration `100_harness_state.sql` (`runs.harness_state`, saved with the cursor); `scripts/real_harnesses.py`; recorded `testdata/claude-code-real.jsonl` (277 lines) and `codex-real.jsonl` (689 lines); table tests. Also the Codex `config.toml` file-secret fix (finding 1 below). |
| §1 settings/UI | `b634c578` | Domain `harnesses.ts` (`harnessSchema`, `resolveHarness`, `harnessMisfit`, labels); `agentModelConfigSchema.harness` is the enum; settings API returns/accepts `harness`; `HarnessField` above Model with source/Reset/inherited and inline attention callout; `TierChip` gains `harness`; fake lux dialect (`fakelux/dialect.go`) plays Claude Code / Codex; Go integration tests; docs. |
| §3 image | `8a82d75f` | Claude Code 2.1.207 and Codex 0.144.1 from their npm platform tarballs (native binaries) into `/usr/local`; `bwrap` from Codex's bundle on PATH. |
| E2E | `98d9975a` | Settings API + UI E2E for Harness; per-harness delivery + chat E2E (parametrised). |
| test fixups | `f6f08304` | `runScreen.test.tsx` chip expectations now lead with "OpenCode ·"; `migrate.test.ts` lists migration 100. |

## Proof 1 — spec goldens (unit)

`TestEachHarnessesSpecIsTheGoldenOne` writes `testdata/spec-harness-{opencode,claude-code,codex}.golden`: each harness × {claude-sonnet-5, gpt-6-sol} × effort {NULL, none, high}, with headers `{"X-Team":"dude","anthropic-beta":"context-1m"}` and (non-OpenCode) options `{"args":["--extra","x y"],"sendReasoning":true}`. A model the harness cannot run holds the refusal text. Summary of the goldens (env shown without the colour vars):

```
claude-code | claude-sonnet-5 effort=     | cmd ["claude","--model","claude-sonnet-5","--permission-mode","bypassPermissions","--thinking-display","summarized","--extra","x y"]
claude-code | claude-sonnet-5 effort=high | cmd [... "--thinking-display","summarized","--effort","high","--extra","x y"]
claude-code | claude-sonnet-5 effort=none | cmd [... "--thinking-display","summarized","--thinking","disabled","--extra","x y"]
   env {"ANTHROPIC_BASE_URL":"https://llm.example","ANTHROPIC_CUSTOM_HEADERS":"X-Team: dude\nanthropic-beta: context-1m",
        "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS":"1","CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1","DISABLE_AUTOUPDATER":"1"}
   secrets GIT_TOKEN, DUDE_TOOLS_AUTH, ANTHROPIC_API_KEY (env)
claude-code | gpt-6-sol (all efforts) | REFUSED: The Implementer runs on Claude Code, which takes an Anthropic model (claude-…), but its tier Coder requests gpt-6-sol. An admin picks another harness or tier in Agents.
codex | claude-sonnet-5 (all efforts)  | REFUSED: The Implementer runs on Codex, which takes an OpenAI model, but its tier Coder requests claude-sonnet-5. An admin picks another harness or tier in Agents.
codex | gpt-6-sol effort=high | cmd ["codex","-c","approval_policy=\"never\"","-c","sandbox_mode=\"danger-full-access\"","-c","check_for_update_on_startup=false",
   "-c","model=\"gpt-6-sol\"","-c","model_provider=\"dude\"",
   "-c","model_providers.dude={name=\"dude\", base_url=\"https://llm.example/v1\", env_key=\"OPENAI_API_KEY\", wire_api=\"responses\", http_headers={\"X-Team\"=\"dude\", \"anthropic-beta\"=\"context-1m\"}}",
   "-c","model_reasoning_summary=\"auto\"","-c","model_supports_reasoning_summaries=true","-c","model_reasoning_effort=\"high\"","--extra","x y"]
codex | gpt-6-sol effort= / none | same without model_reasoning_effort
   env {} ; secrets GIT_TOKEN, DUDE_TOOLS_AUTH, CODEX_CONFIG (file, /home/agent/.codex/config.toml), OPENAI_API_KEY (env)
opencode | both models, all efforts | OPENCODE_CONFIG_CONTENT exactly as before (opencode path untouched)
```

OpenCode goldens unchanged: `git diff 3f1daa07 -- orchestrator/internal/phases/testdata/spec-{opencode,scripted,network,registry,scripted-network}.golden` is empty, and `TestOpenCodeNamedOrNotIsTheSameSpec` proves naming OpenCode builds a `reflect.DeepEqual` spec to naming none. Other unit tests: state volume covers `~/.claude`/`~/.codex`; the key is only the harness's own env secret (not in env/labels/command); `ANTHROPIC_BASE_URL` drops `/v1`; args/ignored options; resume keeps harness; scripted agent's adapter per harness.

## Proof 2 — translator table tests on recorded lines

```
--- PASS: TestARealClaudeCodeTurnIsTranslated
--- PASS: TestARealCodexTurnIsTranslated
--- PASS: TestEachHarnessesToolsAreNamedAsTheChatKnowsThem
--- PASS: TestCodexsEditsAndFailuresAreRecordedAsTheChatKnowsThem
--- PASS: TestClaudeCodesTodoWriteIsThePlan
--- PASS: TestClaudeCodesTasksAndCostOutliveARestart
--- PASS: TestAFailedTurnOnEitherHarnessFailsTheRun
--- PASS: TestARepeatedToolStartIsOneCall
```

- Claude (recorded, claude-sonnet-5 high): thought with text; final message contains "software factory"; `read`, `bash` (stdout apart from stderr), `write`, `edit`, `emit_event` (from `mcp__dude__emit_event`) each called and completed with output; plan from TaskCreate/TaskUpdate, last plan all completed; no `Task*`/`mcp__*` names leak as tools; one turn end with output/cacheRead tokens, `costUsd` > 0, `contextTokens` > 0, matching `runs.agent_cost_usd`/`output_tokens`; no open calls left.
- Codex (recorded, gpt-6-sol high): thought (reasoning summary) with text; messages; `bash` with exit code 0 and output; `emit_event` from `mcpToolCall` server `dude`; plan statuses pending/in_progress/completed (Codex's `inProgress` mapped); one turn end with input/output/cacheRead.
- Table (synthesised in recorded shapes): Claude `Read→read, Bash→bash, Edit→edit, Write→write, Grep→grep, Glob→glob, mcp__dude__ask_person→ask_person, mcp__github__get_issue→github_get_issue, SomethingNew→SomethingNew`; Codex `commandExecution→bash` (command from `commandActions`, not the login-shell wrapper), `fileChange→edit` (diff as output), `mcpToolCall` dude→tool name, other→`server_tool`; failures with status error / exit code / Codex error message; TodoWrite as plan.

## Proof 3 — E2E (scripted)

Go integration (`orchestrator/harnesses_test.go`, fake lux speaking each protocol):

```
--- PASS: TestAScriptedDeliveryOnEachHarness/claude-code   (implement→review→fix→review→simplify, all runs.harness=claude-code, adapter claude-code;
--- PASS: TestAScriptedDeliveryOnEachHarness/codex          thought, message, read call+completion, plan, 2 agent.custom.progress via dude's tools, turn end tokens)
--- PASS: TestAScriptedAgentOnEachHarnessAsksAndCarriesOnWithTheAnswer/{claude-code,codex}
--- PASS: TestAScriptedAgentOnEachHarnessIsSteered/{claude-code,codex}   (accepted while bash runs, not read; read when the command ends)
--- PASS: TestARoleOnAHarnessThatCannotRunItsModelFailsSayingWhy/{claude-code,codex}   (fails before lux)
--- PASS: TestARealModelOnClaudeCodeGoesToLuxAsClaudeCode
```

Python E2E (through the deployed processes and the browser):

```
suites/test_settings.py::test_a_roles_harness_is_layered_like_its_machine_and_opencode_by_default PASSED
suites/test_settings.py::test_a_roles_harness_is_picked_inherited_overridden_and_warned_of_when_its_tier_does_not_fit[chromium] PASSED
suites/test_web_ui.py::test_a_role_on_claude_code_or_codex_delivers_and_its_chat_shows_its_work[chromium-claude-code-Claude Code] PASSED
suites/test_web_ui.py::test_a_role_on_claude_code_or_codex_delivers_and_its_chat_shows_its_work[chromium-codex-Codex] PASSED
==== 4 passed, 75 deselected in 37.89s ====
```

The UI one covers inherit (fixer "The implementer’s · Claude Code"; project "From <org> · Claude Code"), override (Codex, "Overridden <org>: Claude Code Reset"), reset, and the inline misfit callout (Claude Code over a tier requesting an OpenAI model; saved anyway; gone on Codex, back after Reset). The chat one checks phases, `runs.harness`, the chip's `run-model-harness` "Claude Code ·"/"Codex ·", the message, the thought and a `data-tool='read'` card.

## Proof 4 — real CLIs, real proxy

`DUDE_LLM_KEY=… uv run scripts/real_harnesses.py` (key read from auth.json into env, never printed; recordings grep-checked for the key and any prefix of it: 0 hits). The script prints buildSpec's command/env/secrets (via `TestPrintAHarnessSpec`), runs the CLI exactly as lux's adapter would extend it, serves a stand-in for dude's MCP (`emit_event`, `list_tasks`), and pipes the recorded stream through the translator (`TestPrintATranslation`).

```
== claude-code, try 1
   claude --model claude-sonnet-5 --permission-mode bypassPermissions --thinking-display summarized --effort high
   env: {'ANTHROPIC_BASE_URL': 'https://llmproxy.absmartly-dev.com', 'CLAUDE_CODE_DISABLE_BACKGROUND_TASKS': '1', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1', 'DISABLE_AUTOUPDATER': '1'} secrets: ['ANTHROPIC_API_KEY']
   277 records -> orchestrator/internal/phases/testdata/claude-code-real.jsonl; MCP calls: ['emit_event']
   dude events: {'agent.plan.updated': 16, 'agent.thought': 5, 'agent.message': 4, 'agent.tool.called': 8, 'agent.tool.completed': 8, 'agent.model.request.completed': 1}
   thought: I don't have Grep or Glob tools, so I'll use rg via shell instead, ... I'll start by reading the README, grepping, globbing, and checking git log in parallel since these are independent tasks.
   message: Starting with the independent read-only steps (2–5). I have no dedicated Grep or Glob tool, so I'll use `rg` in the shell for those.
   tool: read {"file_path": ".../README.md", ...}
   tool: bash {"command": "rg -n 'harness' docs/operations.md", ...}
   tool: bash {"command": "git log --oneline -1", ...}
   tool: write {"content": "first\n", "file_path": "/tmp/hx-scratch/notes.txt"}
   tool: edit {"file_path": "/tmp/hx-scratch/notes.txt", "new_string": "second", "old_string": "first", "replace_all": false}
   tool: emit_event {"data": {"done": 1, "of": 1}, "type": "progress"}
   thought: I need to check validity: the object must have no key failing the allowed character regex, ...
   message: I've finished all eight steps. I changed nothing in the repository; ...
   turn end: {"contextTokens": 49147, "costUsd": 0.33354195, "tokens": {"cacheRead": 223814, "cacheWrite": 49145, "input": 3048, "output": 4864}, "turn": true}
   OK: claude-code

== codex, try 1
   codex -c approval_policy="never" -c sandbox_mode="danger-full-access" -c check_for_update_on_startup=false -c model="gpt-6-sol" -c model_provider="dude" -c model_providers.dude={name="dude", base_url="https://llmproxy.absmartly-dev.com/v1", env_key="OPENAI_API_KEY", wire_api="responses"} -c model_reasoning_summary="auto" -c model_supports_reasoning_summaries=true -c model_reasoning_effort="high"
   env: {} secrets: ['CODEX_CONFIG', 'OPENAI_API_KEY']
   689 records -> orchestrator/internal/phases/testdata/codex-real.jsonl; MCP calls: ['emit_event']
   dude events: {'agent.message': 3, 'agent.plan.updated': 7, 'agent.model.request.completed': 15, 'agent.tool.called': 6, 'agent.tool.completed': 6, 'agent.thought': 2}
   message: I’ll run the tool checks in order, then inspect the validator.
   tool: bash {"command": "sed -n '1,20p' README.md"}
   tool: bash {"command": "rg -n harness docs/operations.md"}
   tool: bash {"command": "git log --oneline -1"}
   thought: **Planning tool updates**  I need to create a plan for updates before moving to the next step. ...
   tool: emit_event {"data": {"done": 1, "of": 1}, "type": "progress"}
   thought: **Planning concise analysis**  ...
   turn end: {"contextTokens": 17762, "tokens": {"cacheRead": 139520, "cacheWrite": 0, "input": 75264, "output": 1846}, "turn": true}
   OK: codex
```

Codex needed 1 try in this run but 3 tries failed the thought check in the run before it (`real4.log`): gpt-6-sol returned `reasoning` items with empty `summary` on those turns (same as `real_thinking.py` warns for light reasoning). The config does request and declare summaries; the summaries do come when the model writes them. The script retries up to 5 times.

## Gates (final commit `f6f08304`)

- `gofmt -l orchestrator`: empty. `go -C orchestrator vet ./...`: clean.
- Go suite `DUDE_TEST_PG=127.0.0.1:55951 go -C orchestrator test -count=1 -p 4 -timeout 40m ./...` (run on the Go code identical to the final commit; the later commits touch only TS/tests): **28 packages ok, 0 FAIL**, 7 without tests; root package 1272.9 s. `no test database` skips: **0** (also checked with `-v` over the DB-heavy packages: 0).
- `bun run typecheck`: all four packages exit 0.
- Bun tests (domain, design-system, web, control-plane) after `bun run migrate`: **2316 pass, 0 fail**.
- Full E2E `uv run --directory tests python run_tests.py` (Bun 1.4.2 first on PATH, PG 127.0.0.1:55951): **447 passed, 1 skipped** (`test_structure.py:354`, needs a local test gateway — the documented expected skip), 13 deselected (the `lux` contract suites, deselected by default).
- `hx-gate-pg` removed (`docker rm -f`). The temporary `dude-runtime:main-cmp` image was removed; `dude-runtime:hx` is kept.

### Test-ID diff against the base (`3f1daa07`)

Go (`go test -list`): base 1070, branch 1095. **Removed: none.** Added (25): TestAFailedTurnOnEitherHarnessFailsTheRun, TestAHarnessThatCannotRunItsTiersModelIsSaidSo, TestARealClaudeCodeTurnIsTranslated, TestARealCodexTurnIsTranslated, TestARealModelOnClaudeCodeGoesToLuxAsClaudeCode, TestARepeatedToolStartIsOneCall, TestAResumeKeepsTheHarnessItWasSubmittedOn, TestARoleOnAHarnessThatCannotRunItsModelFailsSayingWhy, TestARolesHarnessIsLayeredAndOpenCodeByDefault, TestAScriptedAgentOnEachHarnessAsksAndCarriesOnWithTheAnswer, TestAScriptedAgentOnEachHarnessIsSteered, TestAScriptedDeliveryOnEachHarness, TestATiersArgsAreTheOnlyOptionsOtherHarnessesTake, TestClaudeCodeIsGivenTheProxysURLWithoutV1, TestClaudeCodesTasksAndCostOutliveARestart, TestClaudeCodesTodoWriteIsThePlan, TestCodexsEditsAndFailuresAreRecordedAsTheChatKnowsThem, TestEachHarnessesSessionIsOnAStateVolume, TestEachHarnessesSpecIsTheGoldenOne, TestEachHarnessesToolsAreNamedAsTheChatKnowsThem, TestOpenCodeNamedOrNotIsTheSameSpec, TestPrintAHarnessSpec, TestPrintATranslation, TestTheKeyIsEachHarnessesOwnSecretAndNowhereElse, TestTheScriptedAgentSpeaksItsRolesHarness.

Bun (JUnit ids, both trees run): base 2311, branch 2316. **Removed: none.** Added (5): runScreen "and the harness that ran it, first: Claude Code · Coder · claude-sonnet-5 · high", "the scripted agent names no harness"; domain harnesses.test.ts × 3. Changed expectations (not ids): three runScreen chip tests now expect the "OpenCode ·" prefix; migrate.test.ts lists 100.

Python E2E: added 3 test functions (4 ids with parametrisation); none removed.

## Image size

`scripts/runtime-image.sh dude-runtime:hx` (arm64): **3.23 GB** (content 826 MB) vs base Dockerfile built the same way **2.46 GB** (619 MB). +0.77 GB: `claude` 256 MB, Codex vendor dir ~306 MB (`codex` 259 MB, `codex-code-mode-host` 43.5 MB, bwrap/rg/zsh), layers. In the image: `claude 2.1.207`, `codex-cli 0.144.1`, `opencode 1.18.35` (OpenCode is unpinned `latest` in the existing Dockerfile; docs say so). Verified in the image that `codex app-server` with the config.toml and a trailing `-c mcp_servers…` starts a thread on `modelProvider: dude`.

## Decisions and findings the brief did not cover

1. **Codex 0.144 drops `-c` overrides given before `app-server` once any `-c` follows it.** lux's codex adapter appends `app-server -c mcp_servers.dude.url=…` (lux `internal/adapter/codex.go:92-103`), so with the brief's command Codex ran on its defaults (`openai`, `gpt-5.6-sol`, `on-request`) and went to api.openai.com (401). Probed: before-only works; before + one after reverts everything; all-after works. Not a lux change: dude also writes the same settings as `~/.codex/config.toml`, a file secret `CODEX_CONFIG` (tmpfs, symlinked, resupplied on resume), which Codex reads regardless. The `-c` flags are kept as the brief specifies (they are harmless and document the run). lux could fix this at the root by inserting its overrides before the subcommand; not done (lux is read-only here).
2. **Codex app-server gives a model it does not know no `apply_patch` tool.** gpt-6-sol under app-server lists `exec_command`, `update_plan`, MCP… but no patch tool; `features.apply_patch_freeform=true` did not change that (tested and removed); `codex exec` does have it. So real Codex Runs on gpt-6-sol edit through the shell. The `fileChange → edit` mapping is tested from the schema shape, not a real recording. A fix likely needs a `model_catalog_json` entry for the proxy's model names; left as a follow-up.
3. **Claude Code 2.1.207 plans with `TaskCreate`/`TaskUpdate`, not `TodoWrite`**, and offers no `Grep`/`Glob` tools in this configuration (it uses `rg` via Bash). The translator handles both TodoWrite and Task*; Task* state (numbered tasks) is kept in `runs.harness_state` so a restart mid-turn keeps the plan. Grep/Glob mapping is in the table test only.
4. **Cost.** Claude Code's `result.total_cost_usd` is a running total per *process* (probed: two turns 0.0396 → 0.0430; a `--resume` restarts at 0.0033). The turn's `costUsd` is the delta, or the whole value when it goes down (new process). `result.usage` is per turn (probed). Codex reports no cost; lux's cost plugins price it. Codex tokens are summed from each `thread/tokenUsage/updated.last` over the turn; `input` excludes cached (dude's Anthropic-style split). Both need state across batches → migration 100 `runs.harness_state` (jsonb).
5. **`options.args`**: a list of strings appended to the command. It is the one shape that maps onto both CLIs without dude knowing each flag, and it cannot collide with OpenCode's model options (no OpenCode option is called `args`). Other keys on Claude Code/Codex are logged at Info (`the tier's options other than args are OpenCode's; ignored on this harness`) with the key names. Non-string list entries are dropped.
6. **`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`** is set; the real run with it worked (thinking, tools, MCP, cost). Nothing observed broken.
7. **Codex also gets `check_for_update_on_startup=false`** (auto-update off, per §3).
8. **Session chip**: OpenCode Runs now also lead with "OpenCode ·" (consistent naming of the harness); the scripted agent and Runs with no recorded harness show none.
9. **Fit check** applies to new submits only; a resume keeps the harness it was submitted on (`runs.harness`), whatever the role says now.
10. **Fake lux**: the scripted agent's turns are written once as ACP updates; `fakelux/dialect.go` re-says each in the Claude/Codex shape when the spec's adapter is `claude-code`/`codex`. The scripted model never becomes a misfit.
11. **The domain `harness` field became an enum** (was free `z.string()`). Existing stored values: the old E2E fixture stored `"opencode"`; any other stored string would now fail project/org schema parsing in the backend. I found none in code paths, but a deployment with an odd value would need cleaning; the orchestrator ignores unknown values.
12. **Partial-message streaming for Claude Code** (`--include-partial-messages`) not done, per §5 — follow-up. Whole messages are recorded.
13. The `/tmp/hx-scratch/notes.txt` scratch file used by the real-run script is outside the repo.

Follow-ups: the lux `-c` ordering (1); Codex patch tool for proxy model names (2); partial-message streaming for Claude Code; rebase onto current `github/main` (#76, #89).
