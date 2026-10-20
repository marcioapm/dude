-- dude:no-transaction
-- 168_events_run_chat_idx.sql — run_chat's count and page of a Run's
-- conversation: its WHERE (agenttools/runchat.go, runChatShown), so a busy
-- Run's tool calls and thoughts are not scanned for its few messages.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_chat_idx ON events (run_id, cursor)
  WHERE event_type IN ('agent.message', 'chat.message', 'run.steered', 'question.answered')
    AND (event_type <> 'agent.message' OR btrim(COALESCE(payload->>'text', '')) <> '');
