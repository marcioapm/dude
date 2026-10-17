-- dude:no-transaction
-- A brainstorm session's GitHub reads in the last hour (agenttools'
-- checkGitHubRead): its read attempts by time, without walking the
-- session's other events. Built concurrently, as events are written
-- throughout; IF NOT EXISTS makes a retry after a crash a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_github_reads_idx ON events (session_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted'
    AND payload->>'tool' IN ('github_search', 'github_issue', 'github_pull_request');
