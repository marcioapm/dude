-- The values a Run's text is redacted with (orchestrator/internal/redact):
-- its copy of what it was given (129), and the current value of each name
-- it recorded (128), which still catches a value after the copy is
-- deleted unless it was rotated since. Empty for a role that may not read
-- both tables (the sweeper, 051), rather than an error that would abort
-- the caller's transaction: such a role writes no agent text.
CREATE FUNCTION run_secret_values(rid text) RETURNS TABLE (name text, value text)
LANGUAGE plpgsql STABLE AS $$
#variable_conflict use_column
BEGIN
  IF NOT (has_table_privilege('run_project_secrets', 'SELECT') AND has_table_privilege('project_secrets', 'SELECT')) THEN
    RETURN;
  END IF;
  RETURN QUERY
    SELECT s.name, s.value FROM run_project_secrets s WHERE s.run_id = rid
    UNION
    SELECT p.name, p.value FROM runs r
      JOIN project_secrets p ON p.project_id = r.project_id AND p.name = ANY (r.project_secret_names)
    WHERE r.id = rid;
END $$;
