-- dude:no-transaction
-- 197_events_session_tool_attempts_idx.sql — a session's counted tool
-- attempts by time, whatever the tool: the hourly GitHub reads
-- (agenttools/github.go, githubReadsInHour, which filters its tools from
-- these rows) and chat_history's reads (chathistory.go) both read it, and
-- a tool added later needs no index of its own. Built beside 163's, which
-- 200 drops once this one is valid (the migrator refuses to record a
-- concurrent build that left it invalid).
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_tool_attempts_idx ON events (session_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted';
