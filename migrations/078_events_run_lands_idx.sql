-- dude:no-transaction
-- Where a Run's harness last said input lands: the steer tool's answer
-- (delivery.ConductSteer) reads the latest accepted directive or delivered
-- prompt of the Run that says so, which events_run_cursor_idx finds only by
-- walking past every other event of the Run. Built concurrently, so events
-- are written throughout; a file of its own, outside a transaction. IF NOT
-- EXISTS makes a retry after a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_lands_idx ON events (run_id, cursor)
  WHERE event_type IN ('run.directive.accepted', 'agent.prompt.delivered') AND payload ? 'lands';
