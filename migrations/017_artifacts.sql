-- Artifacts: files an agent publishes for people — a design, a report,
-- notes, a screenshot — kept with the work item that asked for them.
--
-- lux collects what an agent writes into $LUX_ARTIFACTS every time its
-- container exits, and keeps the bytes; dude records what there is and who
-- made it (the Run, and through it the work item), and streams the bytes
-- from lux when someone opens one. So storage_key is lux's artifact id:
-- never a URL, never a credential (plan §66).

-- Which start of the lux Run published it: a resumed Run exits again, and
-- what it publishes then is a later version.
ALTER TABLE artifacts ADD COLUMN epoch integer NOT NULL DEFAULT 1;

-- One row per lux artifact, however many times the collector looks.
CREATE UNIQUE INDEX artifacts_storage_key_idx ON artifacts (organization_id, storage_key);

ALTER TABLE runs
  -- When the Run's container stopped with what it published still to be
  -- recorded: set when it stops — finished, paused, aborted or failed — and
  -- cleared once lux has reported everything that exit kept.
  ADD COLUMN artifacts_due_at  timestamptz,
  -- When to ask lux next: its report of an exit trails the exit itself.
  ADD COLUMN artifacts_next_at timestamptz;

CREATE INDEX runs_artifacts_next_idx ON runs (artifacts_next_at) WHERE artifacts_due_at IS NOT NULL;

-- Every way a Run's container stops passes through one of these statuses,
-- so no path can forget to collect what it published.
CREATE FUNCTION runs_artifacts_due() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.artifacts_due_at := now();
  NEW.artifacts_next_at := now();
  RETURN NEW;
END $$;

-- Also when a lux Run is recorded on a Run that has already stopped: an
-- abort that raced the submit, whose status change came first.
CREATE TRIGGER runs_artifacts_due
  BEFORE UPDATE OF status, lux_run_id ON runs
  FOR EACH ROW WHEN (NEW.lux_run_id IS NOT NULL AND NEW.status IN ('completed', 'failed', 'aborted', 'paused')
                     AND (OLD.status IS DISTINCT FROM NEW.status OR OLD.lux_run_id IS NULL))
  EXECUTE FUNCTION runs_artifacts_due();
