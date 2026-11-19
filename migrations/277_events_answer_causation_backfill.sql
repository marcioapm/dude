-- 277_events_answer_causation_backfill.sql — define the resumable backfill.
--
-- The procedure is defined separately from its CALL because PostgreSQL only
-- permits COMMIT in a procedure called at the top level. The Bun runner sends
-- each no-transaction file as one SQL payload; 279 is therefore the standalone
-- CALL and 280 removes this temporary procedure.
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
