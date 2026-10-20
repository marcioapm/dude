-- dude:no-transaction
-- 165_drop_events_session_github_reads_idx.sql — the count no longer
-- matches 148's WHERE; 163's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_session_github_reads_idx;
