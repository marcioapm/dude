-- dude:no-transaction
-- Completed Runs whose lux Run dude has still to terminate (phases'
-- RetireCompleted): lux keeps a stopped or succeeded Run, and its storage,
-- until it is terminated, and dude never resumes a completed one. Built
-- concurrently, as 076, so runs are read and written throughout.
CREATE INDEX CONCURRENTLY IF NOT EXISTS runs_retirable_idx ON runs (ended_at)
  WHERE status = 'completed' AND lux_run_id IS NOT NULL AND lux_stop_reason IS DISTINCT FROM 'cancel'
    AND artifacts_due_at IS NULL;
