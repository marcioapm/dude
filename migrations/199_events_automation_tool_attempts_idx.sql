-- dude:no-transaction
-- 199_events_automation_tool_attempts_idx.sql — an automation's counted
-- tool attempts by time, whatever the tool, as 197 is a session's. Keyed
-- by the organisation, not payload->>'automationId': under row security
-- the count runs with a qual on organization_id, and ->> is not leakproof,
-- so an expression key on it is never an index condition there. The count
-- filters its automation from the organisation's few attempts of the hour.
-- Replaces 186's, which 202 drops.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_automation_tool_attempts_idx ON events (organization_id, occurred_at)
  WHERE event_type = 'agent.tool.attempted' AND payload ? 'automationId';
