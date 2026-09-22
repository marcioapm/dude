-- 010_workspace_affinity.sql — a Run's workspace lives on one node.
--
-- Resume sets a Run back to `pending` so it can be re-claimed. But the Session
-- Workspace is a directory on one worker's disk, and nothing recorded which.
-- A different worker claiming a resumed Run found no workspace, materialized a
-- fresh clone, and the agent continued against a clean tree — every
-- uncommitted change silently gone, with the Run looking perfectly healthy.
--
-- Invisible with a single runner. Data loss with two.
--
-- The fix is to record where the workspace lives and refuse to schedule the
-- Run anywhere else while it is still there.

ALTER TABLE runs
  -- The node holding this Run's workspace. Set when the workspace is first
  -- materialized, and deliberately NOT cleared on resume: `worker_id` says
  -- who is executing now, `home_worker_id` says where the work lives.
  ADD COLUMN home_worker_id text REFERENCES workers(id) ON DELETE SET NULL,

  -- Whether the workspace can be rebuilt elsewhere without losing anything.
  --
  -- True when every change is committed and pushed to the mirror: a fresh
  -- clone reproduces it. False the moment the agent has uncommitted work.
  -- The reaper uses this to decide between migrating a Run and escalating
  -- it (plan §32).
  ADD COLUMN workspace_portable boolean NOT NULL DEFAULT true;

-- The scheduler asks "what can this worker claim?", which is now partly a
-- question about affinity.
CREATE INDEX runs_home_worker_idx ON runs (home_worker_id)
  WHERE home_worker_id IS NOT NULL;

COMMENT ON COLUMN runs.home_worker_id IS
  'Worker whose disk holds this Run''s Session Workspace. A Run with a '
  'non-portable workspace may only be claimed by this worker.';

COMMENT ON COLUMN runs.workspace_portable IS
  'Whether the workspace can be rebuilt on another node without data loss. '
  'False once the agent has uncommitted changes.';
