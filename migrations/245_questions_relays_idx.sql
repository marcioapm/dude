-- dude:no-transaction
-- questions_relays_fkey's (242): a questions delete finds the questions
-- that relay it without scanning questions. Partial: matches no row until a
-- later PR writes relays.
-- Built concurrently, as 243; IF NOT EXISTS makes a retry a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS questions_relays_idx ON questions (relays)
  WHERE relays IS NOT NULL;
