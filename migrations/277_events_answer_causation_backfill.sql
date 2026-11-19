-- dude:no-transaction
-- 277_events_answer_causation_backfill.sql — bounded historical backfill.
--
-- 273's compatibility trigger prevents new writers from introducing NULL
-- rows. This idempotent statement updates the existing qualifying rows and is
-- safe to retry if a migration client disconnects after the UPDATE.
-- dude:no-transaction
WITH candidates AS (
  SELECT id
  FROM events
  WHERE event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed')
    AND causation_id IS NULL
    AND COALESCE(payload->>'answerId', (payload->'github')->>'answerId') IS NOT NULL
  ORDER BY cursor
)
UPDATE events e
SET causation_id = COALESCE(e.payload->>'answerId', (e.payload->'github')->>'answerId')
FROM candidates
WHERE e.id = candidates.id;