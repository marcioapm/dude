-- dude:no-transaction
-- 182_drop_events_asks_idx.sql — the notifier's query no longer matches the
-- old asks predicate (090); 181's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_asks_idx;
