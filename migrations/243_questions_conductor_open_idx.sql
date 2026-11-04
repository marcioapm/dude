-- dude:no-transaction
-- The deadline sweep's: a task's open questions with its conductor (242's
-- addressee). Matches no row until a later PR writes 'conductor'.
-- Built concurrently, so questions is read and written throughout; a file
-- of its own, outside a transaction. IF NOT EXISTS makes a retry after a
-- crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS questions_conductor_open_idx ON questions (task_id)
  WHERE status = 'open' AND addressee = 'conductor';
