-- dude:no-transaction
-- A fixer's held answer's settling records (#485), by Run and the answer
-- that caused them (273): delivery.answerSettled for every held answer
-- selected, and again under the task's Chat lock before each record.
-- Keyed on causation_id, a column (leakproof equality: an index condition
-- under RLS). The settling types only, literally the queries': the claims
-- (thousands on a Run that has retried) have 275 of their own, so an
-- anti-join over a Run's settling records never reads its claims. Replaces
-- 269 (dropped by 276, once 275 is built). Built concurrently, so events are written
-- throughout; a file of its own, outside a transaction. IF NOT EXISTS
-- makes a retry after a crash before it was recorded a no-op. RLS and
-- grants are the table's: an index adds neither.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_causation_idx ON events (run_id, causation_id)
  WHERE event_type IN ('chat.message', 'pull_request.action', 'pull_request.answer_failed');
