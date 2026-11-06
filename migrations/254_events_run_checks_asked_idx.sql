-- dude:no-transaction
-- The checks gate's asks to the conductor on a Run: ChecksWaiting
-- (delivery's OpenAsk) reads them for every live Run on every sweep, and
-- the gate for its Run on each pass, which events_run_cursor_idx finds only
-- among every other event of each Run. Partial on the literal type
-- ChecksWaiting names, so a generic plan proves the predicate. Built
-- concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before
-- it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_checks_asked_idx ON events (run_id, cursor)
  WHERE event_type = 'run.checks.asked';
