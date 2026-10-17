-- dude:no-transaction
-- A task's conductor GitHub reads in the last hour, across its Runs
-- (agenttools' checkGitHubRead); see 134.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_github_reads_idx ON events (task_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request');
