-- dude:no-transaction
-- 185_drop_events_asks_idx.sql — the notifier's query no longer matches the
-- old asks predicate (090); 184's index serves it.
DROP INDEX CONCURRENTLY IF EXISTS events_asks_idx;
