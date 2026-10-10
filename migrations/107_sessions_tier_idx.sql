-- dude:no-transaction
-- The sessions that chose a tier (106's column), by it: a tier's removal
-- finds them (106's trigger, and its ON DELETE SET NULL). Built
-- concurrently, so sessions are read and written throughout; a file of its
-- own, outside a transaction (the runner's no-transaction marker above).
-- IF NOT EXISTS makes a retry after a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS sessions_tier_idx ON sessions (tier) WHERE tier IS NOT NULL;
