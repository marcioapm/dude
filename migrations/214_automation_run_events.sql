-- 214_automation_run_events.sql — an automation run's activity in the ledger.
--
-- An automation run (automation_runs, ids "arn_…") is not a Run (runs): its
-- agent's messages, thoughts and tool calls, a command's or server's output
-- lines, and the deliveries sent to it are events keyed by
-- automation_run_id. Such an event names no Run, session or task: the
-- check refuses one that does, as the other scope columns are kept apart.
-- NOT VALID: it binds every row written from now on without scanning the
-- table; the rows it would judge are the ones moved just below.
ALTER TABLE events ADD COLUMN automation_run_id text;

-- Until now the tool server wrote an automation run's calls with its id in
-- run_id. Those move: a call's record (run_id only) to automation_run_id;
-- create_task's task.created (on its new task) keeps only the task. One
-- pass over the table, at deploy.
UPDATE events SET automation_run_id = run_id, run_id = NULL
  WHERE starts_with(run_id, 'arn_') AND task_id IS NULL AND session_id IS NULL;
UPDATE events SET run_id = NULL
  WHERE starts_with(run_id, 'arn_');

ALTER TABLE events ADD CONSTRAINT events_automation_run_scope
  CHECK (automation_run_id IS NULL OR num_nonnulls(run_id, session_id, task_id) = 0) NOT VALID;

-- Deleting an automation deletes its runs, and with them their activity.
-- The cascade runs as the table's owner, so the ledger stays append-only
-- for dude_app.
ALTER TABLE events ADD CONSTRAINT events_automation_run_fk
  FOREIGN KEY (automation_run_id) REFERENCES automation_runs(id) ON DELETE CASCADE NOT VALID;

-- Retention (automations.Retain, as dude_sweeper, which may not delete
-- from events): the activity of runs whose log is due to go, up to
-- p_limit rows; it says how many went. The run's log_tail is emptied only
-- once none is left.
CREATE FUNCTION purge_automation_run_events(p_before timestamptz, p_limit integer) RETURNS bigint
LANGUAGE sql SECURITY DEFINER
SET search_path = public, pg_temp AS $$
  WITH gone AS (
    DELETE FROM events WHERE cursor IN (
      SELECT e.cursor FROM automation_runs r JOIN events e ON e.automation_run_id = r.id
      WHERE r.created_at < p_before AND r.log_tail IS NOT NULL
      LIMIT p_limit)
    RETURNING 1)
  SELECT count(*) FROM gone
$$;
REVOKE ALL ON FUNCTION purge_automation_run_events(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_automation_run_events(timestamptz, integer) TO dude_sweeper;
