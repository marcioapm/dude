# dude: Claude Code and Codex as first-class harnesses, chosen per agent role

## Why
dude runs every agent on OpenCode. Márcio wants **Claude Code** and **Codex** as equals: each agent role picks its **harness** (OpenCode, Claude Code or Codex), separately from its **model tier**. The harness is how the agent runs; the tier is which model it asks for and how hard it thinks. A role on Claude Code with tier "Coder (High)" runs `claude` with that tier's model and effort.

Claude Code's built-in prompt asks the model to narrate ("Let me read…"), so the chat gets that for free. **No dude prompt changes.**

## Verified facts (2026-10-08/09). Build on them; do not re-derive.
**lux (`main` 6f722bf) already runs both harnesses.** dude only has to ask for them.
- Adapter names, from lux `internal/spec/spec.go`:
  - `claude-code`: default command `claude`, state path `$HOME/.claude`.
  - `codex`: default command `codex`, state path `$HOME/.codex`.
  - `opencode`: default command `opencode acp`.
  - All three steer at `next_step` with read receipts. lux **rejects a spec whose state volumes do not cover the adapter's state paths.**
- `Workload.Command` (already in dude's lux client) replaces the default command.
  - The claude adapter appends `-p --input-format stream-json --output-format stream-json --verbose`, plus `--resume <id>` and `--mcp-config <file>` (dude's MCP servers, from `Workload.MCPServers`).
  - The codex adapter appends `app-server` and passes the MCP servers as `-c mcp_servers.<name>.url=…` and `env_http_headers`. lux writes Codex's `auth.json` from an `OPENAI_API_KEY` secret.
- **lux's own tests run them** (`lux/tests/harnesses.py`):
  - Claude Code: `["claude", "--model", M, "--permission-mode", "bypassPermissions"]`, with secret `ANTHROPIC_API_KEY`, env `ANTHROPIC_BASE_URL`, and `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1`.
  - Codex: `["codex", "-c", "approval_policy=never", "-c", "sandbox_mode=danger-full-access", "-c", 'openai_base_url="<url>"', "-c", "model=<M>"]`, with secret `OPENAI_API_KEY`.
- **Events lux sends dude:**
  - Claude Code: `claude.*` — `claude.system` (init carries `session_id`), `claude.assistant` (content blocks: `text`, `thinking`, `tool_use`), `claude.user` (`tool_result`), `claude.result`, `claude.turn_end` (with usage), `claude.stream_event` (partials, only with `--include-partial-messages`).
  - Codex: `codex.*` — app-server notifications such as `item/started`, `item/completed`, the agent message, reasoning, command execution, MCP tool call and file change items, and `codex.turn_end` (with usage).
  - **Read lux's adapters (`internal/adapter/claude.go`, `codex.go`) and their tests for the exact shapes.** lux is at `~/git/lux` (`origin/main`); read it only, never edit it.
- **Thinking on Claude Code needs `--thinking-display summarized`.** This is a hidden CLI flag. Without it Claude Code sends `thinking: {type: adaptive}` and the thinking text comes back empty. Wire-tested on Claude Code 2.1.207: with the flag, it sent `display: summarized` and 228 characters came back. **Settings and env vars do not do this; only the flag does.**
- **`--effort <low|medium|high|xhigh|max>`** sets `output_config.effort`.
- **Codex 0.144** reasoning:
  - `-c model_reasoning_effort=<e>` and `-c model_reasoning_summary=auto`.
  - Plus `-c model_supports_reasoning_summaries=true`, because Codex does not know `gpt-6-sol` and otherwise sends no reasoning.
  - Use `wire_api = "responses"`. The proxy passes reasoning summaries only on `/v1/responses`.
- **The LLM proxy** takes Anthropic Messages at `$DUDE_LLM_URL/messages` (Claude Code's `ANTHROPIC_BASE_URL` is `$DUDE_LLM_URL` without `/v1`; check exactly which form Claude Code wants) and OpenAI Responses at `$DUDE_LLM_URL/responses`. It needs a named User-Agent; both CLIs send one.

## What to build

### 1. A role's harness (setting + domain + API + UI)
- **The setting:**
  - Add `harness: "opencode" | "claude-code" | "codex"` to `agentModelConfigSchema` (`packages/domain/src/hierarchy.ts`), optional and defaulting to `opencode`.
  - It sits beside `tier`, `machineSize` and `image`, and layers project → organisation → default exactly like `machineSize`.
  - The fixer follows the implementer, as it does for the other settings.
- **Orchestrator:** `delivery.RoleSettings` gains `Harness`, resolved through the same layers (`delivery/settings.go`).
- **Compatibility is checked when a Run is built, not when the setting is saved:**
  - Claude Code takes Anthropic models (`llm.Provider(model) == llm-anthropic`).
  - Codex takes OpenAI models.
  - OpenCode takes both.
  - A role whose harness cannot run its tier's model fails the Run with a clear reason, just as a missing tier does.
  - The settings page warns **inline** when the chosen tier does not fit the harness, and still allows saving it.
- **UI** (`apps/web/src/screens/settingsPages.tsx`, Agents › role):
  - A **Harness** field: Select with OpenCode / Claude Code / Codex, placed above Model.
  - Show the inherited value and source the way Machine and Image do.
  - The session header chip shows the harness, e.g. `Claude Code · Coder · claude-sonnet-5 · high`.
  - Use existing design-system parts only.
  - Update `docs/design/model-tiers.md` and `docs/operations.md`.
- **Runs record it:** `runs.harness` already exists. It must record the real harness.

### 2. The spec per harness (`orchestrator/internal/phases/spec.go`)
- **One function per harness** builds the `Workload` (Adapter, Command), Env, Secrets and Volumes. Keep OpenCode's path byte-identical: its golden specs must not change.
- **Claude Code:**
  - Adapter `claude-code`.
  - Command: `["claude", "--model", <model>, "--permission-mode", "bypassPermissions", "--thinking-display", "summarized", "--effort", <e>]`.
    - Effort `none`: `--thinking disabled` and no `--effort`.
    - Effort NULL: no `--effort`.
  - Env:
    - `ANTHROPIC_BASE_URL` from the LLM URL;
    - `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1`;
    - `DISABLE_AUTOUPDATER=1`;
    - `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, unless it breaks something, in which case say so.
  - Secret `ANTHROPIC_API_KEY` = the LLM key, as env.
  - A state volume covering `$HOME/.claude`.
  - The tier's **headers** become `ANTHROPIC_CUSTOM_HEADERS` (`"Name: value"` lines).
  - The tier's **options** for Claude Code are **extra CLI arguments**. Make `options` accept, for non-OpenCode harnesses, an `args: string[]` key; document it, and ignore other keys with a log line. Keep this simple, and justify the shape in the report.
- **Codex:**
  - Adapter `codex`.
  - Command: `["codex", "-c", "approval_policy=never", "-c", "sandbox_mode=danger-full-access", "-c", "model=<m>", "-c", "model_provider=dude", "-c", 'model_providers.dude={name="dude", base_url="<url>", env_key="OPENAI_API_KEY", wire_api="responses"}', "-c", "model_reasoning_summary=auto", "-c", "model_supports_reasoning_summaries=true", "-c", "model_reasoning_effort=<e>"]`.
    - No `model_reasoning_effort` for `none` or NULL.
    - Use TOML-safe quoting; reuse a helper if lux's `tomlString` shape is needed.
  - Secret `OPENAI_API_KEY`.
  - A state volume covering `$HOME/.codex`.
  - Headers: `-c model_providers.dude.http_headers={…}`.
  - Options `args`: appended.
- **The scripted test models** (`fake/*`) stay on the fake/`acp` path whatever the role's harness. The E2E fake agent needs a way to play Claude Code and Codex. See §4.
- **Egress:** the run's network rules (#81) are unchanged; the LLM host is already allowed.

### 3. The image (`images/runtime/Dockerfile`)
- Install **Claude Code** (pinned version, `npm i -g @anthropic-ai/claude-code@<v>` or the official installer) and **Codex** (pinned `@openai/codex@<v>` or its release binary), system-wide like OpenCode.
- Auto-update is off in both.
- `docs/operations.md` "Agent image contract" lists the three binaries and their versions.
- If the image builder's tests or size checks need updating, do it. **Build the image** with `scripts/runtime-image.sh dude-runtime:hx` and report its size against main's.

### 4. The translator (`orchestrator/internal/phases/translate.go`), the core of the work
- Today it reads only `acp.*`. Add **Claude Code** and **Codex** translation that produces **the same dude events** the chat already renders:
  - `agent.message` (streamed text, flushed as today);
  - `agent.thought`;
  - `agent.tool.called` / `agent.tool.completed` (name, args, result, status, duration);
  - the plan/todo if the harness has one (Claude Code's TodoWrite tool; Codex's plan item);
  - `agent.model.request.completed` with `turn: true` at a turn's end;
  - usage and cost exactly as OpenCode's path records them;
  - the session id for resume.
- **Tool names and args** are normalised to what the chat's tool cards already know:
  - Claude Code: `Read` → read, `Bash` → bash, `Edit`/`Write` → edit/write, `Grep`/`Glob` → grep/glob, `TodoWrite` → the plan.
  - Codex: `commandExecution` → bash, `fileChange` → edit, `mcpToolCall` → the MCP tool.
  - **Look at what the web's tool cards switch on** (`apps/web/src/api/conversation.ts`, `ToolCallCard` `iconFor`) and map onto that. Unknown tools pass through by name.
- **dude's own MCP tools** (`dude event`, `dude ask`, steering receipts, …) must work from both harnesses exactly as from OpenCode. They come through the MCP server lux gives the agent; check the tool-name prefix each harness puts on MCP tools, e.g. Claude Code's `mcp__dude__<tool>`.
- Keep translation per harness in its own file (`translate_claude.go`, `translate_codex.go`), sharing the event-emitting helpers. Table-driven tests feed **real recorded event lines**. Record them from real runs (§6) into `testdata/`; strip anything secret.
- **The fake lux and fake agent:**
  - The scripted E2E agent must be able to play a role on Claude Code and on Codex, by emitting `claude.*` / `codex.*` events in the shapes lux sends.
  - E2E tests then prove a scripted role on each harness: its messages, a thought, a tool call, a custom event, its question, a steer.
  - Extend existing suites; do not add a new suite file unless one is clearly needed.

### 5. Out of scope
- **Prompts:** not one word.
- **lux:** no changes. If something in lux blocks you, stop and report exactly what, with file:line.
- **The proxy:** no changes.
- **Live streaming of partial text for Claude Code** (`--include-partial-messages`): whole messages are fine. Note it as a follow-up.

### 6. Proof required (in the report, with output)
1. **Unit:** the spec for each harness × {Claude, GPT} × {effort NULL, none, high}, with headers and args, as goldens. The OpenCode goldens are unchanged.
2. **Translator:** the table tests on recorded lines, for each harness: a message, a thought, every mapped tool, a TodoWrite/plan, an MCP `dude` tool, the turn end with usage.
3. **E2E** (scripted): a role on Claude Code and a role on Codex each deliver through the normal phases, and their chat shows a message, a thought and a tool call. Plus the settings E2E for the Harness field (inherit, override, the incompatible-tier warning).
4. **Real CLIs, real proxy** (a script under `scripts/`, not CI, Python like `scripts/real_thinking.py`):
   - Run `claude` and `codex` locally with exactly the command, env and secrets the spec builds, on a small read-only question in this repo. Key: `python3 -c 'import json;print(json.load(open("/Users/marcio/.local/share/opencode/auth.json"))["llmproxy-anthropic"]["key"])'` — never print it.
   - Pipe each one's stream through the translator (a Go test or command that reads it) and show it produces `agent.thought` with text, `agent.message` and `agent.tool.called`.
   - This is also where you record the testdata for §4.

## How to work
- **Start editing within 15 minutes.**
  - Read `orchestrator/internal/phases/spec.go`, `translate.go` (all of it), `delivery/settings.go`, `fakelux/fakelux.go`, `fakeagent/fakeagent.go`, `apps/web/src/screens/settingsPages.tsx`, and lux's `internal/adapter/claude.go` and `codex.go`.
  - Order: §2 spec, then §4 translator with recorded lines, then §1 settings, then §3 image, then E2E.
- **Commit after each section once its tests pass.** Push each with `git push github HEAD:<your branch>`. Do NOT open a PR.
- **Gates on the final commit:**
  - Postgres for the gates: the shared one on 55901 is GONE. **Start your own:** `docker run -d --name hx-gate-pg -p 127.0.0.1:55951:5432 -e POSTGRES_USER=dude -e POSTGRES_PASSWORD="$PW" -e POSTGRES_DB=dude pgvector/pgvector:pg17-trixie`. `$PW` is the password dbtest expects: read it from `adminConn` in `orchestrator/internal/dbtest/dbtest.go` into a shell variable, and never print it. Wait for `pg_isready`. **Remove it when you finish** (`docker rm -f hx-gate-pg`). Touch no other container.
  - `gofmt -l orchestrator` empty; `go -C orchestrator vet ./...`.
  - Full Go suite: `DUDE_TEST_PG=127.0.0.1:55951 go -C orchestrator test -count=1 -p 4 -timeout 40m ./...`, with `$HOME/.bun/bin` on PATH. Report the PASS/FAIL counts and the `no test database` count, which must be 0.
  - `bun run typecheck`.
  - Bun tests for domain, design-system, web, and control-plane: `DATABASE_URL=postgres://dude:$PW@127.0.0.1:55951/dude` after `bun run migrate`.
  - Full E2E: `uv run --directory tests python run_tests.py`, with Bun 1.4.2 first on PATH (`/var/tmp/bun-latest/bun-darwin-aarch64`) and `DUDE_TEST_PG_HOST=127.0.0.1 DUDE_TEST_PG_PORT=55951`.
- **Run anything over a minute in the background to a log, and check it every ≤ 3 minutes. Never `sleep` more than 180 s.** A past run slept 20 minutes after its suite had finished.
- **Test IDs:** additions only. Diff `go test -list` and the Bun test names against `github/main`.

## Rules
- The worktree is your current directory. **Use relative paths in every read, grep, glob and edit.** Absolute paths only under `/var/tmp`, `/tmp`, `~/git/lux` (read only), and the one `auth.json` read above.
- **Never `cd`:** use `go -C`, `git -C`, `bun --cwd`, `uv run --directory`. **Edit files only with the edit and write tools:** no `sed -i`, heredocs, shell redirection into tracked files, or `python3 -` edits.
- **Never `git stash`. Never use the task tool or spawn subagents.** Go is on PATH; never search for binaries.
- Delete nothing outside your worktree except your own `hx-gate-pg` container.
- **Report** to `/var/tmp/harnesses-report.md` with the write tool:
  - per section, what changed and its commit;
  - the four proofs, with their output;
  - gate numbers and the test-ID diff;
  - the image size;
  - anything you decided that this brief did not.
