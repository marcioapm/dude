-- dude:no-transaction
-- What settles a fixer's held answer (#485), by Run and the answer it names:
-- its reply's chat.message (github.answerId), its resolution's
-- pull_request.action (answerId), or its pull_request.answer_failed
-- (answerId); and a poster's claim on it, pull_request.answer_claimed
-- (answerId). delivery.answerSettled reads it for every held answer of every
-- selected fix Run, and again under the task's Chat lock before each
-- record; the claim lookup before each post. events_run_cursor_idx finds
-- them only among every other event of the Run. The expression and the
-- types are literally those of delivery.settledAnswerID and the queries, so
-- a generic plan proves the predicate and matches the key; cursor last for
-- the claim lookup's latest-first read. Built concurrently, so events are
-- written throughout; a file of its own, outside a transaction. IF NOT
-- EXISTS makes a retry after a crash before it was recorded a no-op. RLS
-- and grants are the table's: an index adds neither.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_settled_idx ON events
  (run_id, (COALESCE(payload->>'answerId', (payload->'github')->>'answerId')), cursor)
  WHERE event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed', 'pull_request.answer_claimed');
