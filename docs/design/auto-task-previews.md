# Automatic Task Previews

A project setting, off by default: when dude opens a pull request for one of
the project's tasks, or a person reopens one dude opened, dude prepares that
task's preview. Each task may say otherwise.

## Settings

- `projects.auto_task_preview boolean NOT NULL DEFAULT false` (migration
  272). Read and written with the other preview settings:
  `GET /v1/projects/:id/servers` → `previews.autoTaskPreview`,
  `PUT /v1/projects/:id/preview-settings` (the project's editors; replaced
  whole, like every preview setting, so left out it is off).
- `tasks.auto_task_preview boolean NULL`: the task's own choice, null
  following the project's. `GET /v1/tasks/:id` gives `autoTaskPreview` and
  `projectAutoTaskPreview`; `PATCH /v1/tasks/:id` takes
  `autoTaskPreview: true | false | null` from any member, and is never fixed
  by a delivery (it does not change what the task asks). CLI:
  `dude task update TASK --auto-preview on|off|project`.
- In effect: `COALESCE(task, project)`.

## When

- **Opened**: `delivery.Store.openPullRequest`, after the transaction that
  inserted the `pull_requests` row and appended `pull_request.opened`
  commits, and only when that attempt inserted it. A plain replay of the
  step opens nothing (`reposToOpen` counts the recorded open pull request as
  covering the branch). The guard covers the step reaching GitHub for a pull
  request already recorded — e.g. recorded closed, reopened on GitHub before
  a sync, and the branch moved: GitHub hands back the same number, the insert
  conflicts, and nothing is prepared (the reopen's sync is what prepares it).
- **Reopened**: `prs.Syncer.sync`, after the compare-and-set transaction
  that recorded the row going from `closed` to open or draft. Not on any
  other sync: a push (`synchronize`), checks, reviews, an open pull request
  synced again.
- Never for a pull request dude took over from a person (`adopted`), nor a
  branch preview; every tracked pull request has a task.

## What

`servers.Service.AutoPreview` reads the setting in effect and whether the
project's previews are wakeable (lux's relative hostnames or a preview
domain, and a server that starts in previews), then calls `startPreview`
(`StartPreview` without the view it returns, which asks lux) as
`system`/`auto-preview`:

- the task's live preview, if it has one (the conductor's), is returned
  as it is: never a second task preview, never a branch preview;
- otherwise a new one, asleep: a non-agent's preview gets no
  `wake_wanted_at` (`agentWakes`), so no lux Run is placed until someone
  opens its URL, and `announceReady` never says "Ready for your look" for it.

A project whose previews are not wakeable gets none: `startPreview` would
make an eager preview there, whose Run starts at once. That is skipped with
a Debug log, as is a task that is over or gone (`task_finished`,
`not_found`). Any other error is logged at Warn and dropped by the caller:
the pull request's record, already committed, is what matters. A crash
between that commit and the call leaves the pull request without its
automatic preview; nothing reconciles it.

## Screens

- Settings → Servers → Branch previews: **Automatic task previews**
  (Off/On). On, with no server marked to start in previews (or no server at
  all), the row's help adds a hint linking to **Definitions**. It still
  saves; nothing warns at run time.
- Task Overview → This task: **Automatic preview**, `Use project default
  (On|Off)` / `On` / `Off`, rendered with the rail's task-choice component
  (`BeforePullRequestFact`, as Before the pull request is). Its hint says it
  governs only the automatic preparation (a preview someone asks for still
  starts) and ends with `Effective: On|Off · project default|task override`.
  Disabled with the reason once the task is done, as Before the pull request
  is (a failed or aborted task can be picked back up).

Built from existing design-system components (`SettingRow`, `Select`,
`BeforePullRequestFact`); no design-system change.
