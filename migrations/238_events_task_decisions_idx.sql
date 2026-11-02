-- dude:no-transaction
-- A task's decision events: how many decisions in a row a quiet
-- conductor's delivery left to Deliver's defaults
-- (delivery.defaultsInARowTx, read on every quiet decision past its wait
-- and again as a default is recorded), its newest reset read backwards and
-- the defaults since it, which events_task_cursor_idx finds only among
-- every other event of the task. task.decided is every banner decision;
-- the query keeps conductor_unavailable's retry among them.
-- Built concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before it
-- was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_decisions_idx ON events (task_id, cursor)
  WHERE event_type IN ('conductor.decision_defaulted', 'task.decider_changed', 'conductor.decided', 'task.decided');
