-- 196_task_pull_request.sql — a task's own choice of what Deliver does
-- before its pull request: open it, open it as a draft, or ask its owner
-- first. NULL follows the project's delivery policy (`pullRequest`), else
-- the factory's "open". Read when the delivery reaches the point before
-- the pull request, never frozen at the start.
ALTER TABLE tasks ADD COLUMN pull_request_choice text
  CHECK (pull_request_choice IS NULL OR pull_request_choice IN ('open', 'draft', 'ask'));
