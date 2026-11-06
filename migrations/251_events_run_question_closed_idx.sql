-- dude:no-transaction
-- A question's close classification, without scanning its Run's history.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_question_closed_idx
  ON events (run_id, (payload->>'questionId'), cursor)
  WHERE event_type = 'question.closed';
