-- dude:no-transaction
-- 164_events_task_github_reads_v2_idx.sql — the task's hourly GitHub reads,
-- refresh_pull_requests included, as 163 is the session's.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_github_reads_v2_idx ON events (task_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'refresh_pull_requests');
