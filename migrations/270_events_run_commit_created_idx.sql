-- dude:no-transaction
-- A Run's publications (git.commit_created), by Run: the posting of a
-- fixer's held answers (#485) reads each fix Run's published heads and its
-- first publication (delivery.fixersToPostTx), and settleHeldOnEndTx the
-- heads. events_task_commit_created_idx is by task; events_run_cursor_idx
-- finds them only among every other event of the Run. Partial on the
-- literal type (delivery.isCommitCreated), so a generic plan proves the
-- predicate. Built concurrently, so events are written throughout; a file
-- of its own, outside a transaction. IF NOT EXISTS makes a retry after a
-- crash before it was recorded a no-op. RLS and grants are the table's: an
-- index adds neither.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_commit_created_idx ON events (run_id, cursor)
  WHERE event_type = 'git.commit_created';
