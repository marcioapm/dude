-- dude:no-transaction
-- 184_events_asks_v2_idx.sql — the notifier's asks with automation.failed
-- (a failed automation run, pushed to its owner or, while held, its
-- organisation's admins). Its predicate is notify.asks, literally: the
-- planner uses a partial index only for a query that says the same. Built
-- concurrently, so the ledger is written throughout; 185 drops the old one.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_asks_v2_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'task.ready_to_merge', 'run.stalled', 'automation.failed');
