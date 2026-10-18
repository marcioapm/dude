-- Independent children retain their authorized parent and requested profile.
-- NULL source_kind denotes existing workflow and interactive Runs.
ALTER TABLE runs
  ADD COLUMN source_kind text CHECK (source_kind IN ('task_conductor', 'session')),
  ADD COLUMN parent_session_id text REFERENCES sessions(id),
  ADD COLUMN child_profile text,
  ADD COLUMN child_prompt text,
  ADD COLUMN child_capabilities jsonb;

ALTER TABLE runs ADD CONSTRAINT runs_child_parent CHECK (
  source_kind IS NULL OR (
    parent_run_id IS NOT NULL AND child_profile IS NOT NULL AND child_capabilities IS NOT NULL
    AND ((source_kind = 'task_conductor' AND task_id IS NOT NULL AND parent_session_id IS NULL)
      OR (source_kind = 'session' AND task_id IS NULL AND parent_session_id IS NOT NULL))
  )
) NOT VALID;
