-- dude:batch-sql
-- 277_events_answer_causation_backfill.sql — bounded historical backfill.
--
-- Fills causation_id on the answer records written before 273's trigger,
-- one batch per transaction (the runner's dude:batch-sql protocol), so no
-- transaction holds more than one batch's row locks or reads more than one
-- window of events. The progress row is this backfill's resume point: a run
-- interrupted between batches starts again from it, never from 0.
-- dude:setup
CREATE TABLE IF NOT EXISTS events_answer_causation_backfill_progress (
  migration_version text PRIMARY KEY,
  last_cursor bigint NOT NULL DEFAULT 0
);
INSERT INTO events_answer_causation_backfill_progress (migration_version)
VALUES ('277') ON CONFLICT (migration_version) DO NOTHING;
-- dude:batch
-- Each batch reads one window of cursors, (last_cursor, last_cursor +
-- 100000], and takes at most __BATCH_LIMIT__ qualifying rows from it, so its
-- read work is bounded however few rows qualify: only history from before
-- 273 is NULL, everything newer the trigger filled.
--
-- The cursor moves past every row a batch takes and never comes back: to
-- the last row taken when the batch is full (the window may hold more),
-- otherwise to the window's end, which the batch has exhausted. A row
-- another transaction holds is waited for (FOR UPDATE), never skipped.
--
-- batch_count is the number of rows taken, or 1 while windows remain before
-- the table's last cursor though this one held none: the runner stops at 0.
WITH bounds AS (
  SELECT p.last_cursor AS lo,
         p.last_cursor + 100000 AS hi,
         (SELECT max(cursor) FROM events) AS tail
  FROM events_answer_causation_backfill_progress p
  WHERE p.migration_version = '277'
), candidates AS (
  SELECT e.id, e.cursor
  FROM events e
  WHERE e.cursor > (SELECT lo FROM bounds)
    AND e.cursor <= (SELECT hi FROM bounds)
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
    -- Holds even without the lock above: a row another writer gave a
    -- causation_id while this batch waited for it keeps that value.
    AND e.causation_id IS NULL
  RETURNING c.cursor
), progress AS (
  UPDATE events_answer_causation_backfill_progress p
  SET last_cursor = CASE WHEN (SELECT count(*) FROM candidates) = __BATCH_LIMIT__
                         THEN (SELECT max(cursor) FROM candidates)
                         ELSE (SELECT hi FROM bounds) END
  WHERE p.migration_version = '277'
  RETURNING p.last_cursor
)
SELECT GREATEST((SELECT count(*) FROM candidates),
                CASE WHEN (SELECT hi FROM bounds) < COALESCE((SELECT tail FROM bounds), 0) THEN 1 ELSE 0 END)::int AS batch_count;
