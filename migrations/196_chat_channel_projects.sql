-- 196_chat_channel_projects.sql — what routing is told about a chat
-- conversation's projects, and the projects a chat integration may never
-- reach.

-- A conversation's "usually about" projects: a prior the routing call is
-- told, never a rule. Set by an admin on the provider's settings page, or
-- from the conversation itself (the channel_projects route). Keyed by the
-- conversation as the provider names it within its workspace.
CREATE TABLE chat_channel_projects (
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider              text NOT NULL,
  external_workspace_id text NOT NULL,
  conversation_id       text NOT NULL CHECK (conversation_id <> ''),
  -- How people read the conversation ("#nightly"), as last seen.
  conversation_name     text NOT NULL DEFAULT '',
  project_id            text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  set_by                text REFERENCES people(id) ON DELETE SET NULL,
  set_at                timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, provider, external_workspace_id, conversation_id, project_id)
);
CREATE INDEX chat_channel_projects_project_idx ON chat_channel_projects (project_id);
ALTER TABLE chat_channel_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_channel_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_channel_projects
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_channel_projects TO dude_app;

-- Projects hidden from a provider's integration: no session, task or epic
-- it starts can see them (routing, linking, every agent tool). Keyed by
-- the organisation and provider rather than the installation, so the fence
-- outlives a reinstall: what an earlier installation started stays fenced.
-- Set by an admin on the provider's settings page only.
CREATE TABLE chat_hidden_projects (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider        text NOT NULL,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  hidden_by       text REFERENCES people(id) ON DELETE SET NULL,
  hidden_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, provider, project_id)
);
CREATE INDEX chat_hidden_projects_project_idx ON chat_hidden_projects (project_id);
ALTER TABLE chat_hidden_projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_hidden_projects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_hidden_projects
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_hidden_projects TO dude_app;
-- The syncer leaves a fenced project's repositories out of a session's
-- checkout, across tenants.
GRANT SELECT ON chat_hidden_projects TO dude_sweeper;

-- The channel_projects route: a mention that sets the conversation's projects.
ALTER TABLE chat_requests DROP CONSTRAINT chat_requests_route_check;
ALTER TABLE chat_requests ADD CONSTRAINT chat_requests_route_check CHECK (route IN ('brainstorm', 'task', 'epic', 'resume', 'task_link',
  'clarify', 'channel_projects', 'not_connected', 'not_member', 'failed'));

-- A conversation's latest requests that made something, newest first: the
-- routing call's learned hint reads the last 20 of one conversation.
CREATE INDEX chat_requests_conversation_idx ON chat_requests (provider, external_workspace_id, conversation_id, created_at DESC)
  WHERE result_id IS NOT NULL;
