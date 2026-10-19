-- 157_task_lifecycle.sql — a person ends a task saying why (Close, a
-- resolution), and puts work away (Archive) (docs/design/tracking-work.md,
-- Epic 1 — Lifecycle).
--
-- Two facts beside a task's status, neither of them a status: why a person
-- ended it, and whether it is out of everyday views. Status stays
-- delivery's; Close and Reopen each make one status move of their own.
--
-- The functions that read the new `duplicates` relation kind are in 158:
-- a value ALTER TYPE … ADD VALUE adds can't be used in the transaction
-- that adds it.
--
-- Lock cost: ADD COLUMN without a default is catalog-only; the two CHECKs
-- scan tasks once under its ACCESS EXCLUSIVE lock, and the backfill
-- updates only done tasks with nothing merged.

CREATE TYPE task_resolution AS ENUM ('wont_do', 'duplicate', 'cant_reproduce', 'nothing_to_merge');

ALTER TABLE tasks
  ADD COLUMN resolution  task_resolution,
  ADD COLUMN closed_by   text REFERENCES people(id) ON DELETE SET NULL,
  ADD COLUMN archived_at timestamptz,
  -- Who archived it, as memories.archived_by: a person's id.
  ADD COLUMN archived_by text;

ALTER TABLE epics
  ADD COLUMN archived_at timestamptz,
  ADD COLUMN archived_by text;

-- Today's "done, nothing merged" gets its name.
UPDATE tasks t SET resolution = 'nothing_to_merge'
 WHERE t.status = 'done'
   AND NOT EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state = 'merged');

-- A resolution is an ending's reason: only on a done or aborted task. An
-- archived task has ended (done, or closed with a resolution), so nothing
-- archived can start.
ALTER TABLE tasks
  ADD CONSTRAINT tasks_resolution_status CHECK (resolution IS NULL OR status IN ('done', 'aborted')),
  ADD CONSTRAINT tasks_archived_ended    CHECK (archived_at IS NULL OR status = 'done' OR resolution IS NOT NULL);

-- The board's "N archived" and the archived list, per project.
CREATE INDEX tasks_archived_idx ON tasks (project_id, archived_at DESC) WHERE archived_at IS NOT NULL;

-- `duplicates`: from a task closed as a duplicate to the one it duplicates.
-- Written only by Close, removed only by Reopen.
ALTER TYPE task_relation_kind ADD VALUE 'duplicates';
