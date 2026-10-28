-- Requests belong to people, not to a workflow step's replaceable snapshot.
CREATE TABLE base_sync_requests (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  pull_request_id text NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
  base text NOT NULL,
  base_sha text NOT NULL CHECK (base_sha <> ''),
  writer_run_id text REFERENCES runs(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  PRIMARY KEY (pull_request_id, base_sha)
);
CREATE INDEX base_sync_pending_idx ON base_sync_requests(task_id) WHERE completed_at IS NULL;
ALTER TABLE base_sync_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE base_sync_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON base_sync_requests
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON base_sync_requests TO dude_app;
