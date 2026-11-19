-- dude:no-transaction
-- 269's expression index, which no query can use under RLS (273): 274
-- and 275 replace it, and it is only write cost on every chat.message and
-- pull_request.action. Dropped concurrently, a file of its own; IF EXISTS
-- makes a retry a no-op.
DROP INDEX CONCURRENTLY IF EXISTS events_run_answer_settled_idx;
