-- dude:no-transaction
-- 163_events_session_github_reads_v2_idx.sql — the session's hourly GitHub
-- reads count refresh_pull_requests too: the index holds what the count's
-- WHERE asks for (agenttools/github.go, githubReadsInHour). Built beside the
-- old one, which migration 165 drops once this one serves the count.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_github_reads_v2_idx ON events (session_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'refresh_pull_requests');
