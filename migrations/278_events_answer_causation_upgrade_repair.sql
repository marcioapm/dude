-- 278_events_answer_causation_upgrade_repair.sql — repair an interrupted
-- 277 run before its top-level CALL. It is intentionally idempotent.
-- dude:no-transaction
CREATE OR REPLACE PROCEDURE backfill_events_answer_causation(p_batch integer DEFAULT 10000)
LANGUAGE plpgsql AS $$
DECLARE
  n integer;
BEGIN
  LOOP
    WITH batch AS (
      SELECT id FROM events
      WHERE event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed')
        AND causation_id IS NULL
        AND COALESCE(payload->>'answerId', (payload->'github')->>'answerId') IS NOT NULL
      ORDER BY cursor
      LIMIT p_batch
      FOR UPDATE SKIP LOCKED
    )
    UPDATE events e
    SET causation_id = COALESCE(e.payload->>'answerId', (e.payload->'github')->>'answerId')
    FROM batch
    WHERE e.id = batch.id;
    GET DIAGNOSTICS n = ROW_COUNT;
    COMMIT;
    EXIT WHEN n = 0;
  END LOOP;
END
$$;
