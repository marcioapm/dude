-- 279_events_answer_causation_backfill.sql — execute the resumable backfill
-- at the top level, as required for procedures that COMMIT.
-- dude:no-transaction
CALL backfill_events_answer_causation();
