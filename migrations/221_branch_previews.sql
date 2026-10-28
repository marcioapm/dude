-- 221_branch_previews.sql — a branch preview of a project, with no task.
--
-- A preview Run names either a task (it previews the task's branch) or a
-- branch of its project (preview_branch): created from the CLI, it follows
-- the branch on every push and ends when the branch is deleted or goes
-- unused for 30 days (servers.Previews).

ALTER TABLE runs ADD COLUMN preview_branch text,
  -- When the daily check last asked the forge whether the branch still exists.
  ADD COLUMN preview_branch_checked_at timestamptz;

-- Exactly one owner of a Run: its task, its session, or a project branch
-- it previews. A branch preview is a preview, of a project.
ALTER TABLE runs DROP CONSTRAINT runs_task_or_session;
ALTER TABLE runs ADD CONSTRAINT runs_task_or_session
  CHECK (num_nonnulls(task_id, session_id, preview_branch) = 1
         AND (task_id IS NULL OR project_id IS NOT NULL)
         AND (preview_branch IS NULL OR (kind = 'preview' AND project_id IS NOT NULL)));

-- One live preview per project branch: two starts reach the same one.
CREATE UNIQUE INDEX runs_live_branch_preview_idx ON runs (project_id, preview_branch)
  WHERE preview_branch IS NOT NULL AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');

-- The events a repository's webhook was last registered with: one
-- registered before dude asked for push and delete (or with no record) is
-- not healthy, and the repair PATCHes it (prs.RegisterWebhooks).
ALTER TABLE repositories ADD COLUMN webhook_events text[];
