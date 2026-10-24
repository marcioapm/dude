-- 194_session_owner_attempts.sql — how often the owner sweep has tried a
-- change and failed. Written in the organisation's own transaction after a
-- failure; at the fifth the sweep logs the change and marks it done
-- (session_owner_done), so one that fails every time cannot hold the
-- watermark for good. Rows at or below the watermark are deleted with the
-- done ones.
CREATE TABLE session_owner_attempts (
  cursor          bigint PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  attempts        int NOT NULL
);
ALTER TABLE session_owner_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_owner_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON session_owner_attempts
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON session_owner_attempts TO dude_app;
GRANT SELECT, DELETE ON session_owner_attempts TO dude_sweeper;
