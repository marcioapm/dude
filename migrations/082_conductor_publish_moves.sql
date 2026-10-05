-- 082_conductor_publish_moves.sql — a conductor's publish is a durable,
-- reconciled operation.
--
-- Once lux has pushed and the comparisons pass, the publish is reserved
-- (moving), under the task's Chat lock, its conductor's Run and its
-- delivery's row, with each repository's intended move in moves:
-- {repo: {repoId, slug, from, base, head, status, error, changedPaths,
-- lines}}, status pending, moved or refused. While a publish is moving no
-- writer starts, its conductor is not replaced or ended, its decider does
-- not change and its task does not end. Each repository's result is kept
-- as it happens, so a retry reconciles what already moved with the forge
-- before anything is judged again.
ALTER TABLE conductor_publishes
  ADD COLUMN workflow_run_id text,
  ADD COLUMN attempt int,
  ADD COLUMN branch text,
  ADD COLUMN moves jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- When lux was asked to push: an ask lux never answers is given up on.
  ADD COLUMN asked_at timestamptz,
  -- The worker's schedule: not before this (a back-off after a transient
  -- failure, or its own claim), and how many transient failures in a row.
  ADD COLUMN next_attempt_at timestamptz,
  ADD COLUMN failures int NOT NULL DEFAULT 0;

ALTER TABLE conductor_publishes DROP CONSTRAINT conductor_publishes_status_check;
ALTER TABLE conductor_publishes ADD CONSTRAINT conductor_publishes_status_check
  CHECK (status IN ('requested', 'asked', 'pushed', 'moving', 'published', 'refused'));

DROP INDEX conductor_publishes_live_idx;
CREATE UNIQUE INDEX conductor_publishes_live_idx ON conductor_publishes (run_id)
  WHERE status IN ('requested', 'asked', 'pushed', 'moving');
-- The worker's queue: live publishes by when they are due.
CREATE INDEX conductor_publishes_due_idx ON conductor_publishes (next_attempt_at NULLS FIRST, created_at)
  WHERE status IN ('requested', 'asked', 'pushed', 'moving');
-- What fences a task's writers: its publish moving now.
CREATE INDEX conductor_publishes_moving_idx ON conductor_publishes (task_id) WHERE status = 'moving';

-- The worker claims publishes across organizations.
GRANT UPDATE (next_attempt_at) ON conductor_publishes TO dude_sweeper;
