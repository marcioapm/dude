-- dude:no-transaction
-- Session child creation keys are unique independently of task-owned keys.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runs_session_creation_key_idx
  ON runs (session_id, creation_key)
  WHERE session_id IS NOT NULL AND creation_key IS NOT NULL;
