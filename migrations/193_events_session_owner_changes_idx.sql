-- dude:no-transaction
-- The orchestrator's owner sweep (192) reads these events past its
-- watermark. Built concurrently, so events are written throughout; a file
-- of its own, outside a transaction. IF NOT EXISTS makes a retry after a
-- crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_owner_changes_idx ON events (cursor)
  WHERE event_type IN ('session.owner_left', 'session.owner_returned', 'person.removed');
