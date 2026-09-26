-- The metrics look for a few kinds of event among the thousands a Run or a
-- task records (tokens, tool calls): these find them without reading the
-- rest. And a task's asks, by task.
CREATE INDEX events_run_parking_idx ON events (run_id, cursor)
  WHERE event_type IN ('run.parked', 'run.unparked');
CREATE INDEX events_task_status_idx ON events (task_id, cursor)
  WHERE event_type = 'task.status_changed';
CREATE INDEX questions_task_idx ON questions (task_id);
CREATE INDEX repository_requests_task_idx ON repository_requests (task_id);
