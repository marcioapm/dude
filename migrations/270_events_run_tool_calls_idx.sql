-- dude:no-transaction
-- A writer Run's shell calls at its latest commit (delivery.RunCallsTx):
-- the checks gate shows the conductor the calls a Run and its subagents
-- started after the commit was made, and run_calls lists them. Read by the
-- Run and the time: events_run_cursor_idx alone walks every event of a
-- busy Run (thousands of reads, thoughts and messages) to find its tool
-- calls, and holds no time. Partial on the literal tool event types, so
-- it holds only them. Concurrently, a file of its own, IF NOT EXISTS, as
-- 255.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_tool_calls_idx ON events (run_id, occurred_at)
  WHERE event_type IN ('agent.tool.called', 'agent.tool.completed', 'agent.child.tool.called', 'agent.child.tool.completed');
