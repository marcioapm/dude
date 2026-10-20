-- dude:no-transaction
-- Session child creation keys are unique independently of task-owned keys.
-- IF NOT EXISTS makes a retry after a build that finished but was not recorded
-- a no-op; a build interrupted mid-way leaves the index INVALID, and 162
-- rebuilds it.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runs_session_creation_key_idx
  ON runs (session_id, creation_key)
  WHERE session_id IS NOT NULL AND creation_key IS NOT NULL;
