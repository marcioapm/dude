-- dude:no-transaction
-- A delivery's end for its fixers' held answers (#485,
-- pull_request.answers_ended), by task: every claim on a held answer
-- reads it under the task's Chat lock (delivery.endedAfter), and the
-- delivery's end before writing another. events_task_cursor_idx finds it
-- only among every other event of the task. Partial on the literal type
-- the queries name, so a generic plan proves the predicate. Built
-- concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before
-- it was recorded a no-op. RLS and grants are the table's: an index adds
-- neither.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_task_answers_ended_idx ON events (task_id, cursor)
  WHERE event_type = 'pull_request.answers_ended';
