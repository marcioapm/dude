-- dude:no-transaction
-- The phase Runs a task's conductor started, by its Run (073's column):
-- the safety net and the Sessions tree look them up. Built concurrently,
-- so Runs are read and written throughout; a file of its own, outside a
-- transaction (the runner's no-transaction marker above). IF NOT EXISTS
-- makes a retry after a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_conductor_run_idx ON runs (conductor_run_id) WHERE conductor_run_id IS NOT NULL;
