-- 139_run_retry_backoff.sql — a phase Run's retry after a failure that may
-- pass (phases.Syncer.retryLater) backs off: 5 s doubling with each failure
-- in a row, up to 5 minutes.
--
-- retry_failures: how many such failures in a row. retry_due_at: when the
-- last of them scheduled the retry. A failure is in a row with the last when
-- it comes within a minute of that retry being due; otherwise the count
-- starts over.
ALTER TABLE runs
  ADD COLUMN retry_failures int NOT NULL DEFAULT 0,
  ADD COLUMN retry_due_at timestamptz;
