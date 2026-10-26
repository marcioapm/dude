-- dude:no-transaction
-- 199_events_automation_tool_attempts_idx.sql — an automation's counted
-- tool attempts by time, whatever the tool, as 197 is a session's.
-- Replaces 186's, which 202 drops.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_automation_tool_attempts_idx ON events ((payload->>'automationId'), occurred_at)
  WHERE event_type = 'agent.tool.attempted';
