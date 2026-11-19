-- dude:batch-sql
-- dude:batch-limit 1000
-- 280_events_answer_causation_cleanup.sql — remove durable backfill state.
-- dude:setup
SELECT 1;
-- dude:batch
SELECT 0::int AS batch_count;
-- dude:cleanup
DROP TABLE IF EXISTS events_answer_causation_backfill_progress;
