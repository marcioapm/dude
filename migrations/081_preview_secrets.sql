-- 081_preview_secrets.sql — a project's preview secrets.
--
-- Environment variables every branch preview of a project gets, as lux
-- secrets (as: env), in its servers and their setup scripts. No agent Run
-- ever gets them. Stored as forge_credentials.secret is (011): plain text,
-- in a table of its own under row-level security, never selected by a
-- route that answers a client; encrypted at rest is a later concern. The
-- sweepers get no grant (051): the preview loop reads them inside the
-- preview's organization.
--
-- A secret's project is its organization's: the composite key makes a row
-- naming another organization's project impossible, whatever the policy
-- lets a transaction write.
ALTER TABLE projects ADD UNIQUE (id, organization_id);

CREATE TABLE project_secrets (
  project_id      text NOT NULL,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- An environment variable's name, as lux takes one (at most 63
  -- characters); never LUX_ (any case). The API refuses dude's own names
  -- (GIT_TOKEN, DUDE_TOOLS_AUTH, DUDE_REGISTRY_AUTH) and names a recipe's
  -- env sets.
  name            text NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$' AND name !~* '^lux_'),
  -- Exactly as given, newlines included. lux refuses an empty one, and a
  -- process's environment cannot hold a NUL (Postgres text cannot either).
  value           text NOT NULL CHECK (value <> '' AND octet_length(value) <= 32768),
  -- What the API shows of it: its last 4 characters (the domain's secretHint).
  hint            text NOT NULL,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text REFERENCES people(id) ON DELETE SET NULL,
  PRIMARY KEY (project_id, name),
  FOREIGN KEY (project_id, organization_id) REFERENCES projects (id, organization_id) ON DELETE CASCADE
);

ALTER TABLE project_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_secrets
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON project_secrets TO dude_app;

-- The secret names a preview's current lux Run was submitted with. lux
-- (v0.1.11) requires a value for each at every resume and ignores a name
-- the Run did not declare, so a resume sends exactly these; one removed
-- from the project since means the Run cannot resume and a new one is
-- submitted. Empty for an agent Run, and for a preview from before this.
ALTER TABLE runs ADD COLUMN preview_secrets text[] NOT NULL DEFAULT '{}';

-- The servers a person added to a preview's lux Run that was replaced
-- (a removed secret, a failed start), as lux's server input: added to the
-- new Run once it is recorded, then cleared. The spec's and a wakeable
-- preview's own servers come with the new Run anyway.
ALTER TABLE runs ADD COLUMN carried_servers jsonb NOT NULL DEFAULT '[]';
