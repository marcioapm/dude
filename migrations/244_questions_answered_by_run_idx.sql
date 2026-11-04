-- dude:no-transaction
-- questions_answered_by_run_fkey's (242): a runs delete finds the questions
-- its Run answered without scanning questions. Partial: matches no row
-- until a later PR writes answered_by_run.
-- Built concurrently, as 243; IF NOT EXISTS makes a retry a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS questions_answered_by_run_idx ON questions (answered_by_run)
  WHERE answered_by_run IS NOT NULL;
