-- Values actually supplied to an agent process, retained only through final
-- output and collection. Like project_secrets (081): plaintext, forced tenant
-- RLS, application access only; client routes must never select this table.
ALTER TABLE runs ADD UNIQUE (id, organization_id);

CREATE TABLE run_project_secrets (
  run_id text NOT NULL,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name text NOT NULL,
  value text NOT NULL CHECK (value <> '' AND octet_length(value) <= 32768),
  PRIMARY KEY (run_id, name),
  FOREIGN KEY (run_id, organization_id) REFERENCES runs (id, organization_id) ON DELETE CASCADE
);

ALTER TABLE run_project_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_project_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON run_project_secrets
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON run_project_secrets TO dude_app;
