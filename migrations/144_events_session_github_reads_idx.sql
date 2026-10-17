-- dude:no-transaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_github_reads_idx ON events (session_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log');
