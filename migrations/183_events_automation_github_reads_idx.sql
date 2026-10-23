-- dude:no-transaction
-- 183_events_automation_github_reads_idx.sql — an automation's hourly GitHub
-- reads (agenttools.githubReadsThisHour), as 163/164 are a session's and a
-- task's: an automation's tool events carry its id in the payload, not in a
-- column. The predicate is githubReadsInHour's, literally.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_automation_github_reads_idx ON events ((payload->>'automationId'), occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request', 'github_check_log', 'refresh_pull_requests');
