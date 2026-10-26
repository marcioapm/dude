-- dude:no-transaction
-- 197_events_session_github_reads_v3_idx.sql — the session's hourly GitHub
-- reads count github_run_jobs too: the index holds what the count's WHERE
-- asks for (agenttools/github.go, githubReadsInHour). Built beside 163's,
-- which migration 200 drops once this one serves the count.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_github_reads_v3_idx ON events (session_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'github_run_jobs', 'refresh_pull_requests');
