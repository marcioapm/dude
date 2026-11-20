-- dude:no-transaction
-- A pull request fixer's held answers (#485) and the records that name one
-- as their cause, by Run: its pull_request.answer_held events (a Run's held
-- answers: delivery.unsettledSQL, fixersToPostSQL, endHeldSQL), and, by
-- causation_id, each answer's claims (pull_request.answer_claimed) and the
-- record that settles it (its chat.message, pull_request.action or
-- pull_request.answer_failed: delivery.answerRecord). events_run_cursor_idx
-- finds them only among every other event of a fixer's Run. causation_id
-- is a plain column (leakproof equality: an index condition under RLS),
-- and both arms of the predicate are implied by the queries' own clauses,
-- so a generic plan uses it. Built concurrently, a file of its own; IF NOT
-- EXISTS makes a retry after a crash before it was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answers_idx ON events (run_id, causation_id, cursor)
  WHERE event_type = 'pull_request.answer_held' OR causation_id IS NOT NULL;
