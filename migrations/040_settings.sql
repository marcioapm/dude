-- Settings in two layers, and prompts with a history.
--
-- The organization sets the defaults every project starts from — each
-- agent role's model, reasoning effort and time limit, and how work is
-- delivered — and a project stores only what it overrides. A key absent
-- from a project's JSON is "from the organization"; one absent from the
-- organization's is the factory's own default (delivery.DefaultPolicy, the
-- built-in prompts). So resetting a value is deleting its key, and a
-- project follows its organization's later changes wherever it has not
-- chosen otherwise.
--
-- Agent roles already live this way: organizations.default_agent_models
-- and projects.agent_models ({ role -> config }). A role's config gains
-- `effort` and `timeLimitMinutes`, and is read field by field rather than
-- whole: a project that changes only the reviewer's effort keeps the
-- organization's reviewer model.

-- The organization's delivery policy, over the factory's defaults; a
-- project's (projects.delivery_policy) is over this one.
ALTER TABLE organizations ADD COLUMN delivery_policy jsonb NOT NULL DEFAULT '{}';

-- The phase syncer reads how long a project's organization lets an agent
-- wait on a person, across organizations.
GRANT SELECT ON organizations TO dude_sweeper;

-- Every save of a role's prompt is a row, so a prompt has a history, and a
-- Run records the version it ran with. The current prompt is the latest
-- row for (organization, project, role): project_id NULL is the
-- organization's, which replaces the built-in prompt; a project's adds to
-- the organization's (or the built-in) or replaces it, by `mode`. An
-- organization that never saves one has no rows and runs the built-in
-- prompt; its first save records the built-in text first, so the history
-- starts where the agents did.
CREATE TABLE prompt_versions (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text REFERENCES projects(id) ON DELETE CASCADE,
  -- The prompt's role: an agent role, or `fixer` — the implementer's
  -- model, told something else.
  role            text NOT NULL CHECK (role IN ('investigator', 'implementer', 'reviewer', 'fixer', 'simplifier', 'qa_browser')),
  -- A project's only. `add` with an empty body is "use the organization's".
  mode            text CHECK (mode IN ('add', 'replace')),
  body            text NOT NULL,
  note            text NOT NULL DEFAULT '',
  -- Who saved it, as the ledger names actors (an API key's id until people
  -- land). NULL for the built-in text an organization's history starts from.
  created_by      text,
  -- The version this one restored, when it is a restore.
  restored_from   text REFERENCES prompt_versions(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK ((project_id IS NULL) = (mode IS NULL))
);

CREATE INDEX prompt_versions_current_idx ON prompt_versions (organization_id, role, project_id, created_at DESC);

ALTER TABLE prompt_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE prompt_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY prompt_versions_isolation ON prompt_versions
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
-- A version is history: it is added to, never edited.
GRANT SELECT, INSERT ON prompt_versions TO dude_app;

-- What each Run was told: the organization's prompt version (NULL for the
-- built-in one) and its project's (NULL for none).
ALTER TABLE runs
  ADD COLUMN prompt_version_id         text REFERENCES prompt_versions(id) ON DELETE SET NULL,
  ADD COLUMN project_prompt_version_id text REFERENCES prompt_versions(id) ON DELETE SET NULL;
CREATE INDEX runs_prompt_version_idx ON runs (prompt_version_id) WHERE prompt_version_id IS NOT NULL;
CREATE INDEX runs_project_prompt_version_idx ON runs (project_prompt_version_id) WHERE project_prompt_version_id IS NOT NULL;

-- An epic is planned, being worked on, or done. NULL is "as its tasks
-- say": done once it has tasks and all of them are finished, active
-- otherwise. A person's choice, once made, stands.
ALTER TABLE epics ADD COLUMN state text CHECK (state IN ('planned', 'active', 'done'));
