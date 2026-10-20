-- dude:no-transaction
-- 162_drop_events_session_github_reads_idx.sql — the count no longer
-- matches 148's WHERE; 160's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_session_github_reads_idx;
