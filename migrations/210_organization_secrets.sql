-- 210_organization_secrets.sql — secrets entered once for the organisation.
--
-- project_secrets (081, 128) one level up: each names which agents get it,
-- in every project of the organisation, and a project secret of the same
-- name takes its place in that project. Stored as project_secrets are:
-- plain text under forced row-level security, never selected by a route
-- that answers a client (the API shows the hint). No sweeper grant (051).
-- docs/operations.md, "Organisation secrets".

-- An organisation secret may be given to no agent: automations that pick
-- it still get it. A project secret keeps needing one (128).
CREATE FUNCTION org_secret_recipients_valid(names text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
  SELECT array_ndims(names) IS NULL
    OR (array_ndims(names) = 1
      AND array_position(names, NULL) IS NULL
      AND names <@ ARRAY['previews', 'tester', 'implementer', 'fixer', 'simplifier', 'reviewer']::text[]
      AND cardinality(names) = (SELECT count(DISTINCT name) FROM unnest(names) name))
$$;

CREATE TABLE organization_secrets (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- As project_secrets.name: an environment variable's name lux takes.
  name            text NOT NULL CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$' AND name !~* '^lux_'),
  value           text NOT NULL CHECK (value <> '' AND octet_length(value) <= 32768),
  hint            text NOT NULL,
  recipients      text[] NOT NULL DEFAULT '{}' CHECK (org_secret_recipients_valid(recipients)),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text REFERENCES people(id) ON DELETE SET NULL,
  PRIMARY KEY (organization_id, name)
);

ALTER TABLE organization_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organization_secrets
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON organization_secrets TO dude_app;

-- The secrets in force for a project (pid NULL: the organisation alone):
-- the project's own, then the organisation's whose name the project does
-- not hold. Every reader that hands values to a Run, a preview or an
-- automation goes through this, so the same-name rule lives in one place.
CREATE FUNCTION secrets_in_force(org text, pid text)
RETURNS TABLE (name text, value text, recipients text[], source text)
LANGUAGE sql STABLE AS $$
  SELECT p.name, p.value, p.recipients, 'project' FROM project_secrets p
    WHERE pid IS NOT NULL AND p.project_id = pid
  UNION ALL
  SELECT o.name, o.value, o.recipients, 'organization' FROM organization_secrets o
    WHERE o.organization_id = org
      AND (pid IS NULL OR NOT EXISTS (SELECT 1 FROM project_secrets p WHERE p.project_id = pid AND p.name = o.name))
$$;

-- ---------------------------------------------------------------------------
-- An automation's picks name their source: both levels can hold a name.
-- ---------------------------------------------------------------------------

-- A pick list of project secret names, as {source: project, name} refs. A
-- dude-role preset picks nothing (it gets what its role is ticked for), so
-- its old picks are dropped. ALTER ... USING rewrites every organisation's
-- rows without reading them through row-level security.
CREATE FUNCTION pg_temp.secret_refs(names text[], preset boolean) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE WHEN preset THEN '[]'::jsonb ELSE COALESCE(
    (SELECT jsonb_agg(jsonb_build_object('source', 'project', 'name', n) ORDER BY n) FROM unnest(names) n), '[]'::jsonb) END
$$;

ALTER TABLE automations ALTER COLUMN secrets DROP DEFAULT;
ALTER TABLE automations ALTER COLUMN secrets TYPE jsonb USING pg_temp.secret_refs(secrets, agent ? 'preset');
ALTER TABLE automations ALTER COLUMN secrets SET DEFAULT '[]'::jsonb;
ALTER TABLE automations ADD CONSTRAINT automations_secrets_refs CHECK (jsonb_typeof(secrets) = 'array');

-- The values an automation's runs are given now: an own agent, a command or
-- a server gets its picks, an organisation pick whose name the project
-- holds resolving to the project's; a dude-role preset gets what its role
-- is ticked for (Tester: tester, Reviewer: reviewer; the Investigator is
-- no recipient), as delivery.SecretRecipients maps a task's roles.
CREATE FUNCTION automation_secrets_given(aid text) RETURNS TABLE (name text, value text)
LANGUAGE sql STABLE AS $$
  WITH a AS (
    SELECT a.organization_id, a.project_id, a.secrets,
      CASE a.agent #>> '{preset,role}' WHEN 'qa_browser' THEN 'tester' WHEN 'reviewer' THEN 'reviewer' END AS recipient,
      a.agent ? 'preset' AS preset
    FROM automations a WHERE a.id = aid AND a.kind <> 'webhook'
  )
  SELECT s.name, s.value FROM a, secrets_in_force(a.organization_id, a.project_id) s
  WHERE CASE WHEN a.preset THEN a.recipient IS NOT NULL AND a.recipient = ANY (s.recipients)
    ELSE EXISTS (SELECT 1 FROM jsonb_array_elements(a.secrets) r
      WHERE r->>'name' = s.name AND (r->>'source' = s.source OR (r->>'source' = 'organization' AND s.source = 'project')))
    END
  ORDER BY s.name
$$;

CREATE OR REPLACE FUNCTION automation_run_secret_values(rid text) RETURNS TABLE (name text, value text)
LANGUAGE plpgsql STABLE AS $$
#variable_conflict use_column
BEGIN
  IF NOT (has_table_privilege('project_secrets', 'SELECT') AND has_table_privilege('organization_secrets', 'SELECT')) THEN
    RETURN;
  END IF;
  RETURN QUERY
    SELECT g.name, g.value FROM automation_runs r, automation_secrets_given(r.automation_id) g WHERE r.id = rid;
END $$;

-- A Run's values (130) also hold the current organisation value of each
-- name it recorded: its copy holds what it was given, and an organisation
-- value under a name the project overrides is redacted too, which is harmless.
CREATE OR REPLACE FUNCTION run_secret_values(rid text) RETURNS TABLE (name text, value text)
LANGUAGE plpgsql STABLE AS $$
#variable_conflict use_column
BEGIN
  IF NOT (has_table_privilege('run_project_secrets', 'SELECT') AND has_table_privilege('project_secrets', 'SELECT')
      AND has_table_privilege('organization_secrets', 'SELECT')) THEN
    RETURN;
  END IF;
  RETURN QUERY
    SELECT s.name, s.value FROM run_project_secrets s WHERE s.run_id = rid
    UNION
    SELECT p.name, p.value FROM runs r
      JOIN project_secrets p ON p.project_id = r.project_id AND p.name = ANY (r.project_secret_names)
    WHERE r.id = rid
    UNION
    SELECT o.name, o.value FROM runs r
      JOIN organization_secrets o ON o.organization_id = r.organization_id AND o.name = ANY (r.project_secret_names)
    WHERE r.id = rid;
END $$;
