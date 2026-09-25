-- An agent asks for a repository its work item does not name (task #45):
-- a person approves or denies; approved, it is added to the work item and
-- to the live Run (lux clones it at the Run's resume, and the agent goes on
-- with its conversation).

CREATE TYPE repository_request_status AS ENUM ('pending', 'approved', 'denied', 'cloned', 'failed', 'cancelled');

CREATE TABLE repository_requests (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  work_item_id    text NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  repository_id   text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  access          repository_access NOT NULL DEFAULT 'read',
  reason          text NOT NULL,
  status          repository_request_status NOT NULL DEFAULT 'pending',
  decided_by      text,
  decided_at      timestamptz,
  -- Why a clone failed, as lux reported it.
  error           text,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX repository_requests_run_idx ON repository_requests (run_id, status);
-- One open request per repository and Run: asking twice waits on the first.
CREATE UNIQUE INDEX repository_requests_open_idx ON repository_requests (run_id, repository_id)
  WHERE status IN ('pending', 'approved');

ALTER TABLE repository_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE repository_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY repository_requests_isolation ON repository_requests
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
GRANT SELECT, INSERT, UPDATE ON repository_requests TO dude_app;
-- The phase syncer finds approved requests to carry out across organizations.
GRANT SELECT ON repository_requests TO dude_sweeper;

-- A request still pending when its Run ends can no longer be decided for
-- it. (An approved one stays approved: the work item names the repository,
-- and every later phase checks it out.)
CREATE FUNCTION cancel_repository_requests() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE repository_requests SET status = 'cancelled' WHERE run_id = NEW.id AND status = 'pending';
  RETURN NEW;
END $$;

CREATE TRIGGER runs_cancel_repository_requests
  AFTER UPDATE OF status ON runs
  FOR EACH ROW WHEN (NEW.status IN ('completed', 'failed', 'aborted') AND OLD.status IS DISTINCT FROM NEW.status)
  EXECUTE FUNCTION cancel_repository_requests();

ALTER TABLE runs
  -- The repositories the lux Run has checked out, by name: what it was
  -- submitted with and what resumes added. An approved repository already
  -- here needs no resume.
  ADD COLUMN lux_repositories text[] NOT NULL DEFAULT '{}',
  -- dude paused the Run to bring approved repositories, and resumes it on
  -- its own; a person's pause is theirs to end.
  ADD COLUMN paused_for_repository boolean NOT NULL DEFAULT false;

-- Which start of the Run (the submit is 0, each resume one more) its tools
-- token is for: a retried submit or resume gets the same token, so the one
-- lux kept from the first attempt still works.
ALTER TABLE runs ADD COLUMN tool_starts integer NOT NULL DEFAULT 0;
