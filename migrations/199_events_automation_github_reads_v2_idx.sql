-- dude:no-transaction
-- 199_events_automation_github_reads_v2_idx.sql — an automation's hourly
-- GitHub reads, github_run_jobs included, as 197 is a session's. The
-- predicate is githubReadsInHour's, literally.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_automation_github_reads_v2_idx ON events ((payload->>'automationId'), occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'github_run_jobs', 'refresh_pull_requests');
