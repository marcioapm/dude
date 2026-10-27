-- dude:no-transaction
-- 215_events_automation_run_idx.sql — an automation run's activity by
-- cursor (214): its page's history and live stream, the runs list's
-- "now:" line and retention. Built concurrently, a file of its own.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_automation_run_cursor_idx ON events (automation_run_id, cursor)
  WHERE automation_run_id IS NOT NULL;
