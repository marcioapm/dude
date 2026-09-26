# Several repositories per task

Status: design, 2026-09-24. Replaces the "one repository per task"
decision in `management.md`, which the owner reversed: tasks name any
number of repositories — none, one, several — and a change that spans
repositories is one task with one PR per repository changed.

## Model

- **`task_repositories (task_id, repository_id, access)`**, with
  `access` = `write` (may change it → may get a PR) or `read` (cloned for
  context, never pushed). Replaces `tasks.repository_id`, which is
  migrated into it as `write`.
- **None named, project has one repository** → that one, write (today's
  behaviour). **None named, project has several** → none: the task
  runs with no checkout (a brainstorm, a design) and its outcome is its
  artifacts. Naming repositories is how a person says "this touches code".
- A task's repositories can change until delivery starts (like its
  goal). Access requests from agents (task #45) add rows later, `read` by
  default.

## A phase Run

- **Clones every repository the task names**, each at its own base:
  the default branch for the implementer, the task's branch head for
  later phases. `spec.git.repositories[]` lists them all, at
  `/workspace/repos/<name>`; the workdir is `/workspace` when there are
  several (the prompt lists them), `/workspace/repos/<name>` when one.
- `git.push.branch` = the per-Run branch, as today. `read` repositories
  carry lux's `git.repositories[].push: false`: lux reports them `skipped`
  and never pushes them (agreed with lux; until it lands, dude ignores
  their push results and deletes any stray per-Run branch it finds).
- **The Run's base is per repository.** `runs.base_ref` becomes
  `runs.base_refs jsonb {repoName: sha}`; `runs.head_sha`/`changed_paths`
  become `runs.heads jsonb {repoName: {sha, changedPaths}}`.

## Publishing

For each `write` repository whose push result moved it (status `pushed`
with a commit different from its base): fast-forward the task's
branch in that repository, delete the per-Run branch, compute changed
paths. Repositories that did not change are left alone — no branch, no PR.
A phase "changed nothing" only if no repository changed and it published no
artifact.

## Review

Reviewers see every repository; `ReviewersFor` runs on the union of changed
paths, each prefixed `<repo>/`. Findings gain `repo` (the column exists).

## Pull requests

- One PR per repository with commits on the task's branch, opened
  together. Each body lists its siblings ("Part of TEXT-12, with
  acme/api#41 and acme/web#88"), updated when a sibling opens.
- **Feedback on any PR wakes one fixer for the task**, which sees
  every repository — a comment on the API PR may need a web change.
- The task is **done when every PR is merged**; a PR closed unmerged
  while others are open escalates ("api#41 was closed; merge the rest?")
  rather than aborting silently.
- Tasks with no changed repository finish at review with their
  artifacts: status `review` → a person marks it done (no PR to merge).

## UI

- Task dialog: a repository multi-select with a read/write toggle per
  repository (write by default), instead of the single Select.
- Task screen: the pipeline's PR step becomes one row per PR; the
  header lists the repositories as chips.

## Order of work

1. Schema + API + dialog (repositories on a task, read/write).
2. Spec with N repositories; per-repository bases and heads; publish per
   repository; review over the union.
3. PR per repository with sibling links; done when all merged; escalation
   on a closed sibling.
4. No repository: the implementer runs without a checkout, nothing to
   push, finishes at review with artifacts.
5. Tests at each step (Go world with two bare repositories; E2E with two
   fake-GitHub repositories; a contract test on real lux with two).
