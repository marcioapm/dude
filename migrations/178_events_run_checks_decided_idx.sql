-- dude:no-transaction
-- A person's checks decisions on a Run: the attempt waivers a later Run's
-- reported failure may carry (delivery.AttemptWaiversTx), read for every
-- Run of the attempt, which events_run_cursor_idx finds only among every
-- other event of each Run. Built concurrently, so events are written
-- throughout; a file of its own, outside a transaction. IF NOT EXISTS makes
-- a retry after a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_checks_decided_idx ON events (run_id, cursor)
  WHERE event_type = 'run.checks.decided';
