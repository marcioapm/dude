-- dude:no-transaction
-- 279_events_answer_causation_backfill.sql — retry the idempotent backfill.
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