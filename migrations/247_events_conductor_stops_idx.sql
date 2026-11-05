-- dude:no-transaction
-- The notifier polls conductor stops globally, independently of task_id.
-- Match its literal predicate without broadening events_asks_v2_idx.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_conductor_stops_idx ON events (cursor)
  WHERE event_type = 'task.decided'
    AND payload->>'by' = 'conductor'
    AND payload->>'action' = 'stop';
