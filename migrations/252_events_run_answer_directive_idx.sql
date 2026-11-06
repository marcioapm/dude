-- dude:no-transaction
-- A Run's answer heard before its loss, by the unread directive it queued:
-- its answer events alone, not the Run's whole history. (Only an index; the
-- query that reads it keeps its cost flat in the Run's answers.)
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_directive_idx
  ON events (run_id, (payload->>'directiveId'))
  WHERE event_type = 'question.answered';
