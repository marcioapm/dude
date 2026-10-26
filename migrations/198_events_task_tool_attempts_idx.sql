-- dude:no-transaction
-- 198_events_task_tool_attempts_idx.sql — a task's counted tool attempts
-- by time, whatever the tool, as 197 is a session's. Replaces 164's, which
-- 201 drops.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_tool_attempts_idx ON events (task_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted';
