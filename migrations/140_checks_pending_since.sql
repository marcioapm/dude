-- 140_checks_pending_since.sql — when a pull request's checks went pending
-- on its current head: what ci_stuck counts its patience from. head_seen_at
-- is when the head was first seen, which may be long before a check
-- started (a later check, a re-run). NULL while the checks are not pending.
--
-- Rows pending now start counting from the migration: their real start is
-- not known, and head_seen_at would escalate a check that started a moment
-- ago at once.
ALTER TABLE pull_requests ADD COLUMN checks_pending_since timestamptz;
UPDATE pull_requests SET checks_pending_since = now() WHERE checks = 'pending';
