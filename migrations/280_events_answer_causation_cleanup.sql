-- 280_events_answer_causation_cleanup.sql — remove the temporary procedure
-- after the backfill has completed.
-- dude:no-transaction
DROP PROCEDURE IF EXISTS backfill_events_answer_causation(integer);
