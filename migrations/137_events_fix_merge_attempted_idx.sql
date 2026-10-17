-- dude:no-transaction
-- A promotion's own merge of its candidate, after GitHub's answer to POST
-- /merges was lost (delivery.fixMergeAttempted): the task's merge-attempt
-- markers, which events_task_cursor_idx finds only among every other event
-- of the task, and events_type_idx only among every marker of the
-- organization. Built concurrently, so events are written throughout; a
-- file of its own, outside a transaction. IF NOT EXISTS makes a retry after
-- a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_fix_merge_attempted_idx ON events (task_id)
  WHERE event_type = 'pull_request.fix_merge_attempted';
