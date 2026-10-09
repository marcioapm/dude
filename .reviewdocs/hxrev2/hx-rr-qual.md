# Harnesses fix-round quality review

## Verdict: REQUEST CHANGES

Three **Medium** findings; no High findings. Scope is only `git diff da210bb4 c843683e`. Working HEAD is `25b0f4c8`, whose only changes after the target are the supplied review documents; feature files match `c843683e`. Read the fix report and all `.reviewdocs/hxrev1` reports. No settled design changes proposed.

## M1 — Tolerant read decoding also accepts invalid new project writes

**Changed location:** `packages/domain/src/hierarchy.ts:74`. Consumers: `apps/control-plane/src/api/routes/projects.ts:52`, `:60`, `:109`, `:215`, `:229`.

`harnessSchema.optional().catch(undefined)` is shared by both stored-object decoding and the project POST/PATCH request schemas. There is no distinction between an unknown value already stored and an invalid value newly submitted. A typo is accepted and silently clears a previously valid project harness override, changing execution to the inherited harness.

**Reproduced through the real Router and database:**

1. POST a project with `agentModels: {reviewer: {harness: "claude-code"}}`: **201**, stored Claude Code.
2. PATCH that fresh project with `agentModels: {reviewer: {harness: "aider"}}`: **200**, persisted/returned `agentModels: {reviewer: {}}` instead of 400.
3. POST another fresh project with `harness: "aider"`: **201**, same silent stripping.
4. Both organization and project settings PATCH with `roles.reviewer.harness: "aider"`: **400**, as required. These strict paths do not protect the project agentModels API.

Evidence: `orchestrator/.taskdocs/api-probe.ts`, `orchestrator/.taskdocs/api.log`.

**Required fix:** keep read normalization separate from strict new-write validation. Normalize stale stored data before the editor round-trips it, or explicitly reconcile a legacy value against the stored row; do not apply unconditional `.catch(undefined)` to public project write bodies. Add API regressions for invalid new POST/PATCH values as well as legacy reads.

## M2 — Older harness_state task lists cannot be updated after upgrading

**Changed locations:** `orchestrator/internal/phases/translate.go:137-142`, `orchestrator/internal/phases/translate_claude.go:276-286`.

Before this fix, persisted `claudeTasks` held only content/status; task identity was its one-based array position. The new decoder copies that array unchanged and defaults `claudeTaskNext` to zero. `TaskUpdate` now searches exclusively for `task["id"]`. Every older entry is unaddressable, and the next TaskCreate is incorrectly assigned ID 1 rather than N+1. This is an orchestrator upgrade/restart within the same Claude session epoch, not a new Claude process, so the epoch reset does not repair it.

**Reproduced by executing the real decoder and plan implementation:** load

```json
{"claudeTasks":[{"content":"one","status":"pending"},{"content":"two","status":"pending"}]}
```

- `TaskUpdate {taskId:"2", status:"completed"}` returns no plan and leaves task two pending.
- Create task three, then update task ID 3: again no plan; persisted task three has **id:"1"**.

Evidence: `TestReviewOldTasksDecode` in `orchestrator/.taskdocs/review_probe_test.go`, failures at `orchestrator/.taskdocs/probe.log:2-3`. The original format is visible in the scoped diff's removed TaskCreate/TaskUpdate implementation.

**Required fix:** on backward decoding, assign missing IDs using the original one-based positions and initialize the next-task counter to the largest loaded ID. Test old-format decode → update → create → serialize/reload → update without a new session epoch.

## M3 — Exact provider-table override bypasses the table guard

**Changed location:** `orchestrator/internal/phases/spec.go:548-561`.

The nested-key guard refuses ancestors and descendants of `model_providers.dude`, but not the exact key. Exact matches go through the ordinary default-replacement branch. Therefore `options.args: ["-c", "model_providers.dude=evil"]` replaces dude's inline provider table with the string `"evil"`, while `model_provider` still selects `"dude"`. The resulting TOML is syntactically valid but has the wrong type for Codex's provider map; this prevents startup rather than rejecting/logging the unsupported override. It also violates the stated guarantee that tier overrides cannot rewrite dude's provider.

**Reproduced:** build settings using the real `codexSettings`, parse with Python `tomllib`, and assert that `config["model_providers"]["dude"]` remains a table. It becomes the string `evil`, and the dropped-args list is empty. A sibling `model_providers.other.name=other` parses correctly; a descendant `model_providers.dude.base_url=evil` is correctly dropped. Thus this is the exact-key boundary, not a blanket prohibition on other providers.

Evidence: `TestReviewConfigTOML` in `orchestrator/.taskdocs/review_probe_test.go`; `orchestrator/.taskdocs/probe.log:8-24`. No live Codex/model call used; the concrete failure is the generated config's parsed provider type.

**Required fix:** refuse exact replacement of protected table-valued defaults as well as ancestor/descendant overrides. Test by parsing the built TOML and checking the provider object, and check dropped-argument logging.

## Fixes verified as sound

- **Claude real ordering:** read-only comparison with `~/git/lux/internal/adapter/claude.go:165-184` confirms `claude.turn_end`, conditional idle, `claude.result`. Translator now waits for both halves, persists half/usage/idle/error with its cursor, defers idle, and emits one completion with that turn's cost/tokens. Existing cross-batch/restart, failure-with-null-result and interrupt tests pass. Fake dialect uses the correct full ordering; the script and recording now put end before result, but omit lifecycle records (Low below).
- **New epoch accounting:** session handling flushes a pending half before resetting the process totals/tasks. Epoch 1 cost .10 then epoch 2 cost .30 produces .40; new task ID 1 updates the new task. Tests pass. New-format retained IDs survive pruning/reload; backward-format compatibility is M2.
- **Codex accounting:** real recording assertions pin input **75264**, cacheRead **139520**, output **1846**. Per-turn sum/reset across restart tests pass.
- **Resume:** both existing call sites retain submitted harness (`orchestrator/internal/phases/sync.go:1000`, `orchestrator/internal/phases/session.go:52`). Both added root integration tests pass and inspect actual `ResumeSecrets` for ANTHROPIC_API_KEY and absence of DUDE_LLM_KEY after changing role to OpenCode; not merely helper returns.
- **Codex config:** supported -c/--config overrides replace scalar defaults, dotted sibling tables and arrays parse through `tomllib`, and descendants of the provider are dropped. Unsupported args have a Warn path called by both spec-building paths. Exact provider replacement is M3.
- **DEL:** domain header validation rejects it; generated config for stored DEL parses and preserves the header value after escaping.
- **Migration/image:** migration 101 applies and `runs_harness_state_check` is catalogued `convalidated=false`; PostgreSQL NOT VALID still enforces new writes. The RUN reorder preserves native packages, vendor contents, bwrap symlink, cleanup, version checks and OpenCode install; no old behavior removed in that diff.

## Brief Lows

- `scripts/real_harnesses.py:162-171` and the Claude recording correct the end/result order but still omit `lux.activity.idle`. The recording test only asserts those two records. The dedicated translator and fake tests cover full end/idle/result, so this is fixture fidelity, not a remaining production ordering failure.
- `TestACodexHeaderWithDELIsValidTOML` checks string spelling instead of actually parsing TOML. The additional review probe parsed the generated config successfully. A parser-based repository test would cover table/value validity rather than formatting alone.
- The task cap only prunes on TaskCreate; lists with no finished tasks can exceed 200, and later completions are not pruned until another create. Not material for this fix round.

## Verification / limitations

- Bun **1.4.2**, supplied PATH. Domain harness/tiers tests: **21 pass, 0 fail**.
- Own scratch PostgreSQL: `hx-rr-qual-pg`, port **55981**, pgvector pg17-trixie, credentials from dbtest.adminConn. Migration runner applied all 89 migrations through 101.
- Go scratch overlay only, no production containers.go edits. The reported supplied overlay was absent in this checkout and scratch searches; a replacement container-check stub was created in ignored scratch solely to compile unrelated phases/root packages on Darwin. Container-image checks are not exercised by these tests.
- Full phases/fakelux run with the backward-load probe: existing tests pass; only the added backward-load probe fails. fakelux package passes. Separate focused probes reproduce M2 and M3. Baseline phases re-run without probes: **PASS** (23.409 s).
- Both resume integration tests: **PASS** (3.805 s). No live model calls, auth-file reads, production fixes, commits or pushes.
- All scratch source and logs are ignored. Removed only own `hx-rr-qual-pg` container. Final `git diff --exit-code` succeeds and `git status --short` is empty: no tracked mutations or untracked nonignored files.
