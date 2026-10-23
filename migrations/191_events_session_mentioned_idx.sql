-- dude:no-transaction
-- The notifier's mention sweep reads session.mentioned events past its
-- watermark, which events_asks_idx (the other asks) does not cover. Built
-- concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before
-- it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_session_mentioned_idx ON events (cursor)
  WHERE event_type = 'session.mentioned';
