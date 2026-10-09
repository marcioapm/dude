-- 101_harness_state.sql — what the translator keeps of a Claude Code or Codex
-- turn between batches.
--
-- OpenCode's events carry their own running totals; these two harnesses
-- report some things only as a whole the translator must remember to read
-- the next part against: Claude Code's cost as a running total for its
-- process and its plan one task at a time (TaskCreate, TaskUpdate); Codex
-- its tokens one model request at a time. Kept with the stream cursor, like
-- agent_message_buffer, so a restart neither repeats nor loses any of it.

ALTER TABLE runs ADD COLUMN harness_state jsonb NOT NULL DEFAULT '{}'
  CHECK (jsonb_typeof(harness_state) = 'object');
