-- dude:no-transaction
-- A Run's answer heard before its loss, by the unread directive it queued,
-- without scanning the Run's history once per answered question.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_answer_directive_idx
  ON events (run_id, (payload->>'directiveId'))
  WHERE event_type = 'question.answered';
