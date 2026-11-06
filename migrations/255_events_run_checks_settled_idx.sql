-- dude:no-transaction
-- What settles a checks ask on a Run (251): the conductor's verdict and the
-- gate's judgement, which ChecksWaiting reads for a Run that has an open
-- ask, and the gate and decide_checks for their Run. Partial on the literal
-- types ChecksWaiting names. Concurrently, a file of its own, IF NOT EXISTS,
-- as 251.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_checks_settled_idx ON events (run_id, cursor)
  WHERE event_type IN ('run.checks.conducted', 'run.checks.judged');
