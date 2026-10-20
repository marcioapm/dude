-- 160_session_child_results.sql: session-owned children have no task; their durable result is session-owned.
ALTER TABLE child_results ALTER COLUMN task_id DROP NOT NULL;
ALTER TABLE child_results ADD COLUMN session_id text REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE child_results DROP CONSTRAINT child_results_source_kind_check;
ALTER TABLE child_results ADD CONSTRAINT child_results_source_kind_check CHECK (source_kind IN ('task_conductor', 'session'));
ALTER TABLE child_results ADD CONSTRAINT child_results_owner_check CHECK (
  (source_kind = 'task_conductor' AND task_id IS NOT NULL AND session_id IS NULL)
  OR (source_kind = 'session' AND task_id IS NULL AND session_id IS NOT NULL)
);
CREATE INDEX child_results_session_idx ON child_results (session_id) WHERE session_id IS NOT NULL;
