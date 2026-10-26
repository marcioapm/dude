-- dude:no-transaction
-- 198_events_task_github_reads_v3_idx.sql — the task's hourly GitHub reads,
-- github_run_jobs included, as 197 is the session's.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_github_reads_v3_idx ON events (task_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'github_run_jobs', 'refresh_pull_requests');
