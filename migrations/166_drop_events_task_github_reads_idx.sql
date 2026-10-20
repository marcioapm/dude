-- dude:no-transaction
-- 166_drop_events_task_github_reads_idx.sql — the count no longer matches
-- 150's WHERE; 164's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_task_github_reads_idx;
