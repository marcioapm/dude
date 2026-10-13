-- dude:no-transaction
-- A peer's last words (`dude task list --peers`, /v1/epics/:id/peers): the
-- latest agent.message of each of a task's Runs, which events_run_cursor_idx
-- finds only by walking back past every other event of a Run that said
-- nothing. Built concurrently, so events are written throughout; a file of
-- its own, outside a transaction. IF NOT EXISTS makes a retry after a crash
-- before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_agent_message_idx ON events (run_id, cursor)
  WHERE event_type = 'agent.message';
