-- dude:no-transaction
-- 200_drop_events_session_github_reads_v2_idx.sql — the count no longer
-- matches 163's WHERE; 197's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_session_github_reads_v2_idx;
