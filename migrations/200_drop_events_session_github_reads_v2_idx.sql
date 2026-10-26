-- dude:no-transaction
-- 200_drop_events_session_github_reads_v2_idx.sql — 197's index serves the
-- session's hourly counts; 163's tool list is no longer the count's.
DROP INDEX CONCURRENTLY IF EXISTS events_session_github_reads_v2_idx;
