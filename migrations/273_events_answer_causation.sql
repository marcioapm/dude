-- 273_events_answer_causation.sql — what settles or claims a fixer's held
-- answer (#485) names it in causation_id, not only in its payload.
--
-- Under row security every query on events carries the tenant qual, and
-- ->> is not leakproof: 269's expression key on payload->>'answerId' is
-- never an index condition there, only a filter over every settling record
-- and claim of the Run (the plan harness: 30,000 rows filtered for one
-- claim). causation_id = $2 is plain text equality, leakproof, so (run,
-- answer) becomes 274's index condition. The writers set it
-- (delivery.answerEvent, RecordReplyTx, RecordPRActionTx); this backfills
-- the records written before them, on 269's partial index, so no answer
-- already settled reads as unsettled (and is posted twice). The payload's
-- answerId stays as it was.
UPDATE events SET causation_id = COALESCE(payload->>'answerId', (payload->'github')->>'answerId')
  WHERE event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed')
    AND COALESCE(payload->>'answerId', (payload->'github')->>'answerId') IS NOT NULL
    AND causation_id IS NULL;
