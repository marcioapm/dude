-- dude:no-transaction
-- A task's git.commit_created events: whether dude pushed an open pull
-- request's head (delivery.unreviewedPRPushesTx, read several times per
-- publish, partly under the task's Chat lock), and a conductor publish's
-- once-per-repository record (phases' publish), which
-- events_task_cursor_idx finds only among every other event of the task.
-- Built concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before it
-- was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_commit_created_idx ON events (task_id)
  WHERE event_type = 'git.commit_created';
