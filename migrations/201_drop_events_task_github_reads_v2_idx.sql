-- dude:no-transaction
-- 201_drop_events_task_github_reads_v2_idx.sql — as 200, for 164's index;
-- 198's serves the count.
DROP INDEX CONCURRENTLY IF EXISTS events_task_github_reads_v2_idx;
