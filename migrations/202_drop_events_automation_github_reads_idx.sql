-- dude:no-transaction
-- 202_drop_events_automation_github_reads_idx.sql — as 200, for 186's
-- index; 199's serves the count.
DROP INDEX CONCURRENTLY IF EXISTS events_automation_github_reads_idx;
