-- 101_harness_state.sql — what the translator keeps of a Claude Code or Codex
-- turn between batches.
--
-- OpenCode's events carry their own running totals; these two harnesses
-- report some things only as a whole the translator must remember to read
-- the next part against: Claude Code's cost as a running total for its
-- process and its plan one task at a time (TaskCreate, TaskUpdate); Codex
-- its tokens one model request at a time. Kept with the stream cursor, like
-- agent_message_buffer, so a restart neither repeats nor loses any of it.

--
-- Catalog changes only: the constant default adds the column without a
-- rewrite, and the check is NOT VALID, so it binds every write from now on
-- without scanning runs under the ACCESS EXCLUSIVE lock. Every existing row
-- holds the default, an object, so there is nothing for it to find.

ALTER TABLE runs ADD COLUMN harness_state jsonb NOT NULL DEFAULT '{}';
ALTER TABLE runs ADD CONSTRAINT runs_harness_state_check
  CHECK (jsonb_typeof(harness_state) = 'object') NOT VALID;
