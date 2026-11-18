-- dude:no-transaction
-- A pull request fixer's held answers (#485, pull_request.answer_held), by
-- Run: the delivery's posting selects the task's fix Runs with one not
-- settled (delivery.fixersToPostTx, on every awaitPRFix and movedOnPR), lists
-- a Run's (unsettledTx), and settles them all when the delivery ends
-- (settleHeldOnEndTx). events_run_cursor_idx finds them only among every
-- other event of each Run (a fixer's thousands), and under RLS a generic
-- plan may take events_type_idx, every held answer of the organization.
-- Partial on the literal type the queries name (delivery.isHeld), so a
-- generic plan proves the predicate. Built concurrently, so events are
-- written throughout; a file of its own, outside a transaction. IF NOT
-- EXISTS makes a retry after a crash before it was recorded a no-op. RLS
-- and grants are the table's: an index adds neither.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_held_idx ON events (run_id, cursor)
  WHERE event_type = 'pull_request.answer_held';
