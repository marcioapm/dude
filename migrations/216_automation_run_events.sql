-- 216_automation_run_events.sql — an automation run's activity in the ledger.
--
-- An automation run (automation_runs, ids "arn_…") is not a Run (runs): its
-- agent's messages, thoughts and tool calls, a command's or server's output
-- lines, and the deliveries sent to it are events keyed by
-- automation_run_id. Such an event names no Run, session or task: the
-- check refuses one that does, as the other scope columns are kept apart.
-- NOT VALID: it binds every row written from now on without scanning the
-- table; the rows it would judge are the ones moved just below.
ALTER TABLE events ADD COLUMN automation_run_id text;

-- What phases.AutomationFeed keeps between batches of an agent's output (the
-- reply and thought being streamed, the calls still open, a harness's turn
-- state), as runs keeps a Run's; and a command's or server's output line
-- not yet ended by a newline.
ALTER TABLE automation_runs ADD COLUMN feed_state jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Until now the tool server wrote an automation run's calls with its id in
-- run_id. Those move: a call's record (run_id only) to automation_run_id;
-- create_task's task.created (on its new task) keeps only the task. Driven
-- from automation_runs through events_run_cursor_idx, so the work follows
-- the number of automation runs, not the size of the ledger.
UPDATE events e SET automation_run_id = e.run_id, run_id = NULL
  FROM automation_runs r
  WHERE e.run_id = r.id AND e.task_id IS NULL AND e.session_id IS NULL;
UPDATE events e SET run_id = NULL
  FROM automation_runs r
  WHERE e.run_id = r.id;

ALTER TABLE events ADD CONSTRAINT events_automation_run_scope
  CHECK (automation_run_id IS NULL OR num_nonnulls(run_id, session_id, task_id) = 0) NOT VALID;

-- Deleting an automation deletes its runs, and with them their activity.
-- The cascade runs as the table's owner, so the ledger stays append-only
-- for dude_app.
ALTER TABLE events ADD CONSTRAINT events_automation_run_fk
  FOREIGN KEY (automation_run_id) REFERENCES automation_runs(id) ON DELETE CASCADE NOT VALID;

-- Retention (automations.Retain, as dude_sweeper, which may not delete
-- from events): up to p_limit activity rows of runs created before
-- p_before, driven from the due runs (automation_runs_activity_due_idx),
-- never from a scan of all automation activity. A due run left with no
-- activity is marked activity_purged_at, so later passes skip it and Retain
-- empties its log_tail. Returns rows deleted plus runs marked.
ALTER TABLE automation_runs ADD COLUMN activity_purged_at timestamptz;
CREATE INDEX automation_runs_activity_due_idx ON automation_runs (created_at) WHERE activity_purged_at IS NULL;

CREATE FUNCTION purge_automation_run_events(p_before timestamptz, p_limit integer) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = public, pg_temp AS $$
DECLARE
  deleted bigint;
  marked bigint;
BEGIN
  WITH due AS (
    SELECT id FROM automation_runs WHERE created_at < p_before AND activity_purged_at IS NULL
    ORDER BY created_at LIMIT p_limit),
  gone AS (
    DELETE FROM events WHERE cursor IN (
      SELECT e.cursor FROM due
      CROSS JOIN LATERAL (SELECT cursor FROM events WHERE automation_run_id = due.id LIMIT p_limit) e
      LIMIT p_limit)
    RETURNING 1)
  SELECT count(*) INTO deleted FROM gone;
  UPDATE automation_runs r SET activity_purged_at = now()
  WHERE r.id IN (SELECT id FROM automation_runs WHERE created_at < p_before AND activity_purged_at IS NULL
                 ORDER BY created_at LIMIT p_limit)
    AND NOT EXISTS (SELECT 1 FROM events e WHERE e.automation_run_id = r.id);
  GET DIAGNOSTICS marked = ROW_COUNT;
  RETURN deleted + marked;
END
$$;
REVOKE ALL ON FUNCTION purge_automation_run_events(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION purge_automation_run_events(timestamptz, integer) TO dude_sweeper;
