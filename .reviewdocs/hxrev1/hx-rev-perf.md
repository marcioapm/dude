# hx-rev-perf — performance review of `e5c52d25..58d91b02`

## Bottom line

**APPROVE.** No High or Medium findings. The translator does linear work per frame. `harness_state`
is a few hundred bytes, written inside the UPDATE that already moves the cursor once per batch.
Migration 101 holds its ACCESS EXCLUSIVE lock for one table scan. The image grows by about 0.56 GB
uncompressed and about 200 MB compressed per arch; nothing safe is left to trim except one optional
44 MB file.

## Findings

None at High or Medium.

## Lows

1. **Migration 101: the inline CHECK scans `runs` under ACCESS EXCLUSIVE.** `ADD COLUMN … NOT NULL
   DEFAULT '{}'` is metadata-only on PG ≥ 11 (a constant default, so no rewrite). The inline
   `CHECK (jsonb_typeof(harness_state) = 'object')` is still validated against every row while the
   lock is held. Measured on `postgres:16` with 500k rows (186 MB heap): **217 ms** with the CHECK
   and **1.9 ms** without it. Adding the constraint `NOT VALID` instead takes 1.4 ms. Every existing
   row holds the constant `'{}'`, so validating them proves nothing. At dude's scale this is a
   sub-second, one-time stall, which is why it is not a Medium. If wanted: `ADD COLUMN … DEFAULT '{}'`,
   then `ADD CONSTRAINT … CHECK (…) NOT VALID` in the same file. Migrations 099 and 100 use the
   same inline-CHECK pattern, so this is an existing convention, not new to this change.
2. **`codex-code-mode-host` (43.5 MB on arm64, `vendor/<triple>/bin/`)** backs Codex's experimental
   `CodeMode` feature (found in the `codex` binary's feature list: `CodeMode`, `CodeModeHost`,
   `CodeModeOnly`). dude does not enable it in `codexWorkload` (`spec.go`). Leaving it out would
   save about 44 MB uncompressed and about 15 MB compressed. The catch: the next Codex bump could
   make it load-bearing, and it would then fail at runtime, not at build time. Optional. If it is
   dropped, the drop should go next to the pinned `CODEX_VERSION`.
3. **The bundled `codex-path/rg` (4.5 MB) and `codex-resources/zsh` (0.9 MB)** duplicate Debian's
   `ripgrep`. That is about 5 MB, not worth diverging from the package's layout. Codex expects
   `bwrap` from `codex-resources`, so it must stay; the image's `/usr/local/bin/bwrap` symlink costs
   nothing.
4. **Cold-host pull.** The npm tarballs are `claude` 80 MB and `codex` 124 MB (arm64), so the
   compressed layer adds roughly 200 MB per arch. It sits in one RUN after the OpenCode layer. An
   OpenCode-only rebuild (`OPENCODE_VERSION=latest`, which is not pinned) invalidates this layer
   too, which is a build-cache cost only. Swapping the two RUNs would let an OpenCode bump reuse
   the pinned Claude Code/Codex layer. Optional.
5. **Large Claude tool results are decoded about three times** (`translate_claude.go:26` into
   `claudeLine` with RawMessage copies, then `claudeOutput` at :189 and :199/201). The content is
   both `message.content[].content` and `tool_use_result`. This is linear and `capOutput` bounds
   what is stored. Fine.
6. **`claudeTasks` has no bound** (`translate_claude.go:249`, `translate.go:101`). It grows with
   each TaskCreate and is never cleared on a new process, a new turn or a resume. Claude Code's own
   task numbering may restart per process (not verified). If it does, a TaskUpdate after a
   `--resume` would hit the old entries. That is a correctness question for the correctness lens.
   For performance: about 60 B per task, so even 1,000 tasks is about 60 KB of jsonb marshalled
   per batch. Not material, but a cap (or a reset when the Claude process changes, which the cost
   logic already detects at `translate_claude.go:297`) would bound it.

## What I checked

- **Translator hot path** (`translate.go`, `translate_claude.go`, `translate_codex.go`):
  - Each frame gets one `json.Unmarshal` into a narrow struct.
  - Nothing appends streamed text. Claude lines are whole (no `--include-partial-messages`).
    Codex's `item/agentMessage/delta` and `summaryTextDelta` (430 + 188 frames in
    `codex-real.jsonl`) hit no case in `codexEvent`, and the whole text is taken from
    `item/completed`. So these paths are not quadratic, and the `strings.Builder` buffers are
    written once and flushed straight away.
  - Plan re-emission clones the task list on each Task* call: O(tasks) per call. Fine.
  - The 216 `claude.system` `thinking_tokens` lines per turn each cost one small decode.
- **`harness_state`:**
  - Marshalled and unmarshalled once per batch (`translate.go` save/load).
  - Written as one extra column in the UPDATE that `save` already ran (`sync.go:1311`). There is no
    extra round trip and no extra DB write.
  - Contents: two floats, at most three int64s (`codexTurn`, reset at each turn end,
    `translate_codex.go:310`) and `claudeTasks` (Low 6).
  - Open tool calls live in the existing `open_tool_calls`, not here.
- **Migration 101:** the PG16 measurements in Low 1. It has a constant default, so there is no
  backfill and no rewrite. The migration runs in a transaction (`apps/control-plane/src/db/migrate.ts`).
- **Spec building** (`spec.go` `claudeCodeWorkload` and `codexWorkload`): a handful of string
  joins, sorted headers and one `json.Marshal` per TOML string, per Run. Negligible.
  `harnessMisfit` and `submittedHarness` are pure. The resume path adds one column to a SELECT it
  already ran.
- **Image:**
  - I downloaded and listed `@openai/codex@0.144.1-linux-arm64`: `codex` 259 MB,
    `codex-code-mode-host` 43.5 MB, `rg` 4.5 MB, `zsh` 0.9 MB, `bwrap` 0.5 MB. The tarball is
    124 MB and Claude Code's is 80 MB.
  - Extraction happens in /tmp and is removed in the same RUN, so the layer holds no leftover
    tarball or temp dir. `cp -r` from /tmp then `rm` happens in a single layer, so nothing is
    duplicated.
  - The other ~0.2 GB of the reported +0.77 GB is not explained by these files: claude 256 MB plus
    codex ≈ 306 MB comes to about 0.56 GB. I did not rebuild the image to pin the cause; the
    likely one is how the docker image store counts disk use. The Dockerfile leaves no files
    behind that would explain it.
- **Web:**
  - `RunTierChip` (`RunScreen.tsx`) does one `in` lookup per render, inside the already memoised
    `RunScreen` header. Nothing new subscribes to the event stream.
  - `HarnessField` does a `tiers.find` per render on the settings page only.
- **Tests:** `go test ./internal/phases` cannot build on macOS (`internal/images` `syscall.Getxattr`,
  as the brief says), so the translator was judged by reading. `./internal/fakelux` builds.
- `git status` is clean. The scratch Postgres container has been removed.
