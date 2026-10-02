-- 068_kept_runs.sql — an aborted or failed Run's lux Run is kept, not
-- cancelled, so a person can resume it where it stopped.
--
-- Until now dude cancelled the lux Run of a Run that failed or was aborted,
-- and its workspace and the agent's conversation went with it. Now a Run
-- worth resuming (keep: a person aborted it, or its agent died) is stopped
-- and kept (lux_stop_reason = 'kept') until kept_until, then cancelled as
-- before ('cancel'). One dude failed for a reason a resume would meet again
-- (lux refused it, a push was denied) is cancelled at once, as before.
-- A Run taken back up goes to paused (lux_stop_reason = 'pause') and on as
-- any paused Run does.
ALTER TABLE runs
  ADD COLUMN keep boolean NOT NULL DEFAULT false,
  ADD COLUMN kept_until timestamptz,
  -- Times it was taken back up after it ended: each new end is a new
  -- phase.finished signal for its workflow, not a repeat of the last.
  ADD COLUMN finishes integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN runs.keep IS
  'Aborted or failed, and worth resuming: its lux Run is stopped and kept rather than cancelled.';
COMMENT ON COLUMN runs.kept_until IS
  'Kept: until when its lux Run is kept for a resume; then it is cancelled.';

-- A Run a person can take back up where it stopped: aborted or failed,
-- worth keeping, and stopped and kept in lux until a time not yet passed.
-- One lux has not yet stopped is not: a resume would wait for a stop
-- nobody asks for. The one definition, for the orchestrator (offering and
-- taking a resume) and the backend (an escalation's actions) alike.
CREATE FUNCTION run_kept(r runs) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT r.status IN ('aborted', 'failed') AND r.keep AND r.lux_run_id IS NOT NULL
    AND r.lux_stop_reason IS NOT DISTINCT FROM 'kept' AND COALESCE(r.kept_until > now(), false)
$$;
