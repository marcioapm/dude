-- dude:no-transaction
-- A delivery's github.authorized signals, newest first (delivery's
-- awaitOwnerAuthorization): a publication parked on its owner's GitHub
-- keys its ask on the latest authorization of theirs the run has seen,
-- read each time it parks. Nothing prunes workflow_signals, and
-- workflow_signals_pending_idx holds only unconsumed signals, so without
-- this the read scans the table. Partial on the literal signal name, so it
-- holds only them. Concurrently, a file of its own, IF NOT EXISTS, as 255.
CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_signals_github_authorized_idx ON workflow_signals (workflow_run_id, received_at DESC)
  WHERE name = 'github.authorized';
