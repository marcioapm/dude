-- dude:no-transaction
-- The "Interrupt now" directives that resend another (062's column), by
-- it: a talker's replay probes, for each message it reads, whether a resend
-- of its directive reached the agent (delivery.heardDirective). Built
-- concurrently, so directives are read and written throughout; a file of
-- its own, outside a transaction (the runner's no-transaction marker
-- above). IF NOT EXISTS makes a retry after a crash before it was recorded
-- a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS directives_resends_idx ON directives (resends) WHERE resends IS NOT NULL;
