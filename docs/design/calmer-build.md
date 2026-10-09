# Calmer: the build brief for every track

Read `docs/design/calmer-plan.md` (the plan), `docs/design/mockups/calmer-team-v8.html`
(the design — open it in a browser; v8 contains everything from v1–v7) and
`docs/CONTRIBUTING.md` (how dude fits together, conventions, traps) first.
`packages/design-system/README.md` is the rulebook: its Philosophy rules 1–12
and the guard tests in `packages/design-system/test/` (radius, borders,
rawValues, rules) are binding. Phase 0 is done on `design/calmer`.

Five tracks build in parallel, each in its own worktree and branch off
`design/calmer`. They merge into `design/calmer` in this order:
**fe → people → settings → github → live**. Keep to your files where you can;
where you must touch a shared file, keep the change small and additive.

## Shared contracts (do not deviate — other tracks rely on them)

### Migrations (numbers are reserved per track)
- people: `035_people.sql` (and `036_*` if needed)
- settings: `040_settings.sql` (and `041_*`)
- github: `045_github.sql` (and `046_*`)
- live: `050_live.sql` (and `051_*`)
- fe: no migrations.
Migrations are append-only raw SQL; enable RLS on every new tenant table
like the existing ones (`ENABLE` + `FORCE ROW LEVEL SECURITY`, policy on
`organization_id = current_setting('app.organization_id')`) and grant to
`dude_app` as earlier migrations do. Copy the pattern from `034_*`/`027_*`.

### People (owned by the people track)
- Table `people` (`id` text pk `per_…`, `organization_id`, `name`, `email`
  citext unique per org, `role` text check in (`admin`,`member`), a photo
  (`photo_key` in the `DUDE_S3_BUCKET`, served under `photo_token`; or an
  https `photo_url`), `last_seen_at` timestamptz null, `created_at`,
  `removed_at` null). `api_keys` gains `person_id` → people. A migration
  backfills one person per existing user key (name from the key's name).
- Every API response that names a person uses the shape
  `{ id: string; name: string; photoUrl: string | null; online: boolean }`
  (`online` = `last_seen_at` within 5 minutes). Type `PersonRef` in
  `packages/domain/src/hierarchy.ts` (people track adds it; fe may add it
  first if it needs it — identical definition).
- `GET /v1/me` → `{ person: PersonRef & { email, role }, organization: { id, name } }`.
- `GET /v1/people` → `{ people: (PersonRef & { email, role, lastSeenAt })[], you: string }`
  (`you` = the caller's person id). Existing callers keep working.
- Tasks: `task_people (task_id, person_id, position, added_at)`; the owner is
  position 0 and stays mirrored in `tasks.owner_key_id` for the orchestrator
  until it moves to persons. Task JSON gains `people: PersonRef[]`
  (owner first) — keep `owner` too.
- Events/ledger: every event with a human actor resolves to a `PersonRef`
  in API output as `actor` (the ledger already stores the key id).

### Settings (owned by the settings track)
- Organisation defaults and project overrides for: agent role config
  (model tier, time limit, enabled, prompt; reasoning effort is the
  tier's, see model-tiers.md) and delivery policy.
  Project stores **overrides only**; absent = inherited. API returns each
  value with `{ value, source: "organization" | "project" }`.
- Prompts: `prompt_versions (id, organization_id, project_id null, role,
  mode 'add'|'replace' for projects, body, note, created_by person, created_at)`.
  Current = latest per (org, project, role). Sessions (runs) record
  `prompt_version_id`.
- Epics gain `state` (`planned`,`active`,`done`), derived default active.
- Routes: `GET/PATCH /v1/settings/organization`, `GET/PATCH
  /v1/projects/:id/settings`, `GET /v1/prompts/:role/history?projectId=`,
  `POST /v1/prompts/:role` (`{ projectId?, mode?, body, note? }`),
  `POST /v1/prompts/versions/:id/restore`.

### GitHub (owned by the github track)
- `pull_requests` gains: `mergeable_state`, `behind_by`, `checks_json`
  (array of `{ name, status, conclusion, url, durationMs }`), `reviews_json`
  (array of `{ login, state, submittedAt }`), `unresolved_threads` int.
- API `PullRequest` gains: `checks`, `reviews`, `mergeable`
  (`clean|behind|conflicting|unknown`), `behindBy`, `unresolvedThreads`, and
  a computed `display: "merged"|"ci_red"|"ci_running"|"awaiting"|"changes"|"conflict"|"comments"|"ready"|"closed"`
  by the priority in the mockups' Rules screen (the fe track renders it;
  put the pure function in `packages/domain` as `prDisplayState(pr)` so fe
  can use it before github lands).
- Routes: `POST /v1/pull-requests/:id/merge` (`{ method }`),
  `POST /v1/pull-requests/:id/update-branch`, `POST /v1/pull-requests/:id/rerun-failed`.

### Live (owned by the live track)
- `GET /v1/runs/:id/diff` → `{ base: string, files: [{ path, status: 'M'|'A'|'D'|'R', additions, deletions, hunks: [{ header, lines: [{ kind: ' '|'+'|'-', old: number|null, new: number|null, text }] }] }], updatedAt }`.
  Pushed as event `run.diff.updated` (payload: the same) over the existing
  SSE stream when the orchestrator refreshes it.
- Cost: `cost_samples`/run usage gains `machine_usd`; API cost fields become
  `{ totalUsd, tokensUsd, machineUsd }` where a single number was shown;
  keep the old field for compatibility.

## Definition of done for a track
1. `bun run typecheck`, `bun test`, `cd orchestrator && go test ./...` pass.
2. Its E2E suites pass (`cd tests && uv run python run_tests.py <suites>`),
   including new tests for what it added. Browser tests for new screens.
3. The design-system guard tests pass (they run in `bun test`).
4. A code-review pass and a simplify pass on its diff, findings fixed.
5. Its screens seen in a real browser, dark and light, against the mockup.
6. Commits in the repo's style (see `git log`): prose explaining the
   decision, ending with the Co-Authored-By line. Push the branch.

## Traps
- Don't `run_tests.py --down` or tear down anything you didn't start; other
  tracks run suites concurrently on this machine. Each `run_tests.py` makes
  its own database — that's fine to run in parallel.
- `dude-postgres` on :5433 is shared; never drop databases you didn't create.
- `networkidle` never fires in the web app (SSE); wait on elements.
