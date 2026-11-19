-- dude:batch-sql
-- dude:batch-limit 1000
-- 277_events_answer_causation_backfill.sql — bounded historical backfill.
-- dude:setup
CREATE TABLE IF NOT EXISTS events_answer_causation_backfill_progress (
  migration_version text PRIMARY KEY,
  last_cursor bigint NOT NULL DEFAULT 0
);
INSERT INTO events_answer_causation_backfill_progress (migration_version)
VALUES ('277') ON CONFLICT (migration_version) DO NOTHING;
-- dude:batch
WITH candidates AS (
  -- Keyset: the progress cursor is a scalar, so it bounds the events_pkey
  -- scan (Index Cond) instead of filtering a scan from the table's start.
  -- A row another transaction holds is waited for, never skipped: the
  -- cursor moves past every row a batch takes and never comes back.
  SELECT e.id, e.cursor
  FROM events e
  WHERE e.cursor > (SELECT p.last_cursor FROM events_answer_causation_backfill_progress p WHERE p.migration_version = '277')
    AND e.event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed')
    AND e.causation_id IS NULL
    AND COALESCE(e.payload->>'answerId', (e.payload->'github')->>'answerId') IS NOT NULL
  ORDER BY e.cursor
  LIMIT __BATCH_LIMIT__
  FOR UPDATE OF e
), updated AS (
  UPDATE events e
  SET causation_id = COALESCE(e.payload->>'answerId', (e.payload->'github')->>'answerId')
  FROM candidates c
  WHERE e.id = c.id
  RETURNING c.cursor
), progress AS (
  UPDATE events_answer_causation_backfill_progress p
  SET last_cursor = COALESCE((SELECT max(cursor) FROM candidates), p.last_cursor)
  WHERE p.migration_version = '277'
  RETURNING p.last_cursor
)
SELECT count(*)::int AS batch_count, (SELECT last_cursor FROM progress) AS last_cursor FROM candidates;
-- dude:cleanup
