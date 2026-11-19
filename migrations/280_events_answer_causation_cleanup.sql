-- dude:no-transaction
-- 280_events_answer_causation_cleanup.sql — final idempotent retry marker.
--
-- The repair is deliberately a no-op: 277–279 are standalone UPDATEs and do
-- not create a procedure that needs cleanup.
-- dude:no-transaction
SELECT 1;