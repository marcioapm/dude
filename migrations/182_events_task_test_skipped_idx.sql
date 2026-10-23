-- dude:no-transaction
-- A task's test.skipped events: the skipped test step whose verdicts a
-- later Run's stand for, read per test Run by GET /v1/tasks/:id/verification
-- and per criterion by delivery.latestVerification, which
-- events_task_cursor_idx finds only among every other event of the task.
-- Built concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before it
-- was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_test_skipped_idx ON events (task_id, cursor)
  WHERE event_type = 'test.skipped';
