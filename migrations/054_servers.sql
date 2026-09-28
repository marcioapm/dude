-- Servers: what a Run can serve, and branch previews.
--
-- A project names its servers once — a port, the command that serves it,
-- where in the checkout it runs, a setup step — and people start them on a
-- task's Run through lux, which gives each its own URL. A branch preview is
-- a Run with no agent: the task's branch, checked out, serving the servers
-- marked to start in previews, parked when nobody has opened it for a
-- while.

-- A project's server recipes. Not lux's: lux knows a Run's servers, and a
-- recipe becomes one when a person adds it to a Run, or when a preview
-- starts.
CREATE TABLE project_servers (
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- Part of its URL (<name>-<run>.<domain>), so lux's rule: a DNS label's
  -- start, and short enough that the label stays under 63.
  name            text NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{0,29}$' AND name !~ '-$'),
  port            integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
  -- A shell command line; run as `sh -c "[setup &&] exec <command>"`.
  command         text NOT NULL CHECK (command <> ''),
  -- Relative to the repository's checkout; '' is its root.
  workdir         text NOT NULL DEFAULT '',
  -- Run before the command on every start (npm ci): NULL for none.
  setup           text,
  -- [{name, value}], not secret: shown to anyone who can read the project.
  env             jsonb NOT NULL DEFAULT '[]',
  autostart_in_previews boolean NOT NULL DEFAULT false,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  updated_by      text REFERENCES people(id) ON DELETE SET NULL,
  PRIMARY KEY (project_id, name)
);

ALTER TABLE project_servers ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_servers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON project_servers
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON project_servers TO dude_app;

-- A recipe as the API shows it. One definition, here, because both
-- processes answer with it: the backend on the project's settings, the
-- orchestrator on a task's servers (the recipes it offers to add).
CREATE FUNCTION server_recipe(s project_servers) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('name', s.name, 'port', s.port, 'command', s.command, 'workdir', s.workdir,
    'setup', s.setup, 'env', s.env, 'autostartInPreviews', s.autostart_in_previews, 'updatedAt', s.updated_at,
    'updatedBy', (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = s.updated_by))
$$;

-- How a project's branch previews run: {image, egress, idleTimeoutMinutes},
-- each key only when set (the image: the project's runtime image; egress:
-- nothing; parked after 30 minutes without a request).
ALTER TABLE projects ADD COLUMN preview_settings jsonb NOT NULL DEFAULT '{}';

-- A project's preview settings with the defaults filled in, as the API
-- shows them (and the orchestrator reads them).
CREATE FUNCTION preview_settings(p projects) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('image', p.preview_settings->'image',
    'egress', COALESCE(p.preview_settings->'egress', '[]'::jsonb),
    'idleTimeoutMinutes', COALESCE((p.preview_settings->>'idleTimeoutMinutes')::float8, 30))
$$;

-- A Run is an agent's (a phase of delivery) or a branch preview's: no
-- agent, the task's branch serving its servers. A preview has no phase, so
-- nothing that drives phases sees it.
ALTER TABLE runs
  ADD COLUMN kind text NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'preview')),
  -- Who asked for it (a preview; an agent Run is its task's).
  ADD COLUMN started_by text REFERENCES people(id) ON DELETE SET NULL,
  -- A preview's last sign of use: when it last started, or the latest
  -- request lux saw to any of its servers. Parked when older than the
  -- project's idleTimeoutMinutes.
  ADD COLUMN active_since timestamptz,
  -- Servers a person started while the preview was parked: started once it
  -- runs again (a spec's servers start on their own).
  ADD COLUMN pending_starts text[] NOT NULL DEFAULT '{}';

-- A preview parked for want of use is dude's pause, like the others: it
-- resumes when a person starts one of its servers.
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused'));

-- One live preview per task.
CREATE UNIQUE INDEX runs_live_preview_idx ON runs (task_id)
  WHERE kind = 'preview' AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused');

-- The preview loop reads previews across organizations, then acts on each
-- in its own; it reads its project's settings from projects (already
-- granted, 021).
CREATE INDEX runs_previews_idx ON runs (created_at) WHERE kind = 'preview';
