-- 180_automations.sql — work nobody started by hand: on a schedule, or when
-- GitHub says something happened. An automation belongs to its organisation
-- (project_id NULL) or to one of its projects, and is one of four kinds: an
-- agent, a command that runs and exits, a server that answers events, or a
-- signed POST to a URL (webhook). docs/CONTRIBUTING.md, "Automations".

CREATE TABLE automations (
  id                text PRIMARY KEY,
  organization_id   text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id        text REFERENCES projects(id) ON DELETE CASCADE,
  name              text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 120),
  description       text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
  kind              text NOT NULL CHECK (kind IN ('agent', 'command', 'server', 'webhook')),
  -- New automations start off: Create opens their settings first.
  enabled           boolean NOT NULL DEFAULT false,
  -- The creator, until a transfer. NULL with owner_left_at set: the owner
  -- left the organisation and its admins hold it together.
  owner_id          text REFERENCES people(id) ON DELETE SET NULL,
  owner_left_at     timestamptz,
  -- {kind: schedule, cron, tz} | {kind: github, event, actions[], filters{branch,label,paths,author}}.
  -- The event is any GitHub event name: the model takes events GitHub adds later.
  trigger           jsonb NOT NULL CHECK (jsonb_typeof(trigger) = 'object' AND trigger->>'kind' IN ('schedule', 'github')),
  session_mode      text NOT NULL DEFAULT 'ephemeral' CHECK (session_mode IN ('ephemeral', 'resumable')),
  -- Days without a run after which the kept session is cleaned up.
  expiry_days       integer CHECK (expiry_days BETWEEN 1 AND 30),
  concurrency       text NOT NULL DEFAULT 'queue'
                    CHECK (concurrency IN ('queue', 'merge', 'skip', 'cancel', 'send', 'parallel')),
  parallel_max      integer NOT NULL DEFAULT 2 CHECK (parallel_max BETWEEN 2 AND 10),
  -- A project automation's checkouts: "all", "none" or an array of repository ids.
  repos             jsonb NOT NULL DEFAULT '"all"'::jsonb,
  -- An organisation automation's {mode: all|only|except, repos: ["owner/name" | "owner/glob-*"]}.
  repo_filter       jsonb,
  -- {own: {harness, tierId, timeLimitMinutes, machineSizeId, imageId, prompt}}
  -- | {preset: {role, overrides}, brief}.
  agent             jsonb,
  machine_size_id   text REFERENCES machine_sizes(id) ON DELETE SET NULL,
  image_id          text REFERENCES images(id) ON DELETE SET NULL,
  setup_script      text CHECK (length(setup_script) <= 65536),
  command           text CHECK (length(command) <= 65536),
  -- {command, port, path, health}.
  server            jsonb,
  -- {url}; the signing secret is kept apart and never read back by the API.
  webhook           jsonb,
  webhook_secret    text,
  webhook_hint      text,
  -- Names of the project's secrets the automation's runs are given.
  secrets           text[] NOT NULL DEFAULT '{}',
  notify_on_failure boolean NOT NULL DEFAULT true,
  -- A schedule's next firing, kept by the orchestrator's scheduler.
  next_run_at       timestamptz,
  created_by        text REFERENCES people(id) ON DELETE SET NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_by        text REFERENCES people(id) ON DELETE SET NULL,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, id),
  CONSTRAINT automations_repo_filter_org_only CHECK (project_id IS NULL OR repo_filter IS NULL),
  CONSTRAINT automations_expiry_resumable CHECK ((session_mode = 'resumable') = (expiry_days IS NOT NULL)),
  CONSTRAINT automations_webhook_ephemeral CHECK (kind <> 'webhook' OR session_mode = 'ephemeral'),
  CONSTRAINT automations_send_resumable_agent CHECK (concurrency <> 'send' OR (kind = 'agent' AND session_mode = 'resumable')),
  CONSTRAINT automations_parallel_ephemeral CHECK (concurrency <> 'parallel' OR session_mode = 'ephemeral'),
  CONSTRAINT automations_server_events CHECK (kind <> 'server' OR trigger->>'kind' = 'github'),
  CONSTRAINT automations_owner_held CHECK (owner_id IS NULL OR owner_left_at IS NULL),
  CONSTRAINT automations_reviewer_pull_request CHECK (
    agent #>> '{preset,role}' IS DISTINCT FROM 'reviewer'
    OR (trigger->>'kind' = 'github' AND trigger->>'event' LIKE 'pull\_request%'))
);
CREATE INDEX automations_org_idx ON automations (organization_id, project_id);
CREATE INDEX automations_owner_idx ON automations (owner_id) WHERE owner_id IS NOT NULL;
CREATE INDEX automations_schedule_due_idx ON automations (next_run_at) WHERE enabled AND next_run_at IS NOT NULL;

-- One firing: what triggered it and what came of it. Queued and skipped
-- rows are kept with their reason, so the Runs tab says why nothing ran.
CREATE TABLE automation_runs (
  id                   text PRIMARY KEY,
  organization_id      text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  automation_id        text NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  status               text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'skipped', 'cancelled')),
  reason               text CHECK (length(reason) <= 2000),
  -- schedule, event (a live delivery), run_now (a person, on a schedule or a
  -- past delivery) or test (a written test event: always a run of its own).
  source               text NOT NULL CHECK (source IN ('schedule', 'event', 'run_now', 'test')),
  -- Every payload the run got, in arrival order: one, or the merged queue.
  -- [{deliveryId, event, action, body}]; emptied after 30 days.
  payloads             jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(payloads) = 'array'),
  payloads_purged_at   timestamptz,
  scheduled_for        timestamptz,
  -- new or resumed for a resumable automation's kept session; ephemeral otherwise.
  session              text CHECK (session IN ('new', 'resumed', 'ephemeral')),
  -- An agent's dude Run (its transcript, tools and cost); a command's or
  -- server's lux Run directly.
  run_id               text REFERENCES runs(id) ON DELETE SET NULL,
  lux_run_id           text,
  started_by           text REFERENCES people(id) ON DELETE SET NULL,
  cost_usd             numeric,
  exit_code            integer,
  -- The last of the log, kept 90 days.
  log_tail             text CHECK (length(log_tail) <= 65536),
  container_started_at timestamptz,
  container_stopped_at timestamptz,
  stop_reason          text CHECK (stop_reason IN ('idle', 'error', 'aborted')),
  -- A Server run's last request answered (or started): the idle clock.
  last_request_at      timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  started_at           timestamptz,
  ended_at             timestamptz,
  UNIQUE (organization_id, id)
);
CREATE INDEX automation_runs_automation_idx ON automation_runs (automation_id, created_at DESC);
CREATE INDEX automation_runs_open_idx ON automation_runs (created_at) WHERE status IN ('queued', 'running');

-- The kept session of a resumable automation: one per automation, resumed
-- by every firing until expiry_days pass without a run (Off does not stop
-- the clock).
CREATE TABLE automation_sessions (
  automation_id   text PRIMARY KEY REFERENCES automations(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE SET NULL,
  lux_run_id      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_run_at     timestamptz NOT NULL DEFAULT now()
);

-- One POST a Server or Webhook run made: its answer, or why there was none.
CREATE TABLE automation_run_requests (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES automation_runs(id) ON DELETE CASCADE,
  delivery_id     text,
  try             integer NOT NULL DEFAULT 1 CHECK (try BETWEEN 1 AND 2),
  http_status     integer,
  ms              integer,
  error           text CHECK (length(error) <= 2000),
  response_excerpt text CHECK (length(response_excerpt) <= 2000),
  at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX automation_run_requests_run_idx ON automation_run_requests (run_id, at);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['automations', 'automation_runs', 'automation_sessions', 'automation_run_requests'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (organization_id = current_organization_id())'
      ' WITH CHECK (organization_id = current_organization_id())', t);
  END LOOP;
END $$;
-- The scheduler, the delivery matcher and the retention sweep read across organisations.
GRANT SELECT, UPDATE ON automations TO dude_sweeper;
GRANT SELECT, UPDATE ON automation_runs, automation_sessions TO dude_sweeper;

-- ---------------------------------------------------------------------------
-- An automation's agent is a Run with no task and no session.
-- ---------------------------------------------------------------------------

ALTER TABLE runs ADD COLUMN automation_id text REFERENCES automations(id) ON DELETE CASCADE;
CREATE INDEX runs_automation_idx ON runs (automation_id, created_at) WHERE automation_id IS NOT NULL;
-- NOT VALID: it only widens runs_task_or_session (automation_id is NULL on
-- every row before it), so validating would scan runs under this ALTER's
-- lock for no row it could refuse.
ALTER TABLE runs DROP CONSTRAINT runs_task_or_session;
ALTER TABLE runs ADD CONSTRAINT runs_task_or_session
  CHECK (num_nonnulls(task_id, session_id, automation_id) = 1 AND (task_id IS NULL OR project_id IS NOT NULL)) NOT VALID;

-- ---------------------------------------------------------------------------
-- Every GitHub delivery is stored now, not only the pull-request ones.
-- ---------------------------------------------------------------------------

-- When the automation matcher read the delivery. Rows from before this
-- migration take its time (a constant default, so no rewrite): an old
-- delivery never fires an automation made after it.
ALTER TABLE webhook_deliveries ADD COLUMN automations_at timestamptz DEFAULT now();
ALTER TABLE webhook_deliveries ALTER COLUMN automations_at DROP DEFAULT;
CREATE INDEX webhook_deliveries_automations_idx ON webhook_deliveries (received_at) WHERE automations_at IS NULL;
-- Past deliveries of one event (Run now), and the events seen (the picker's fallback).
CREATE INDEX webhook_deliveries_event_idx ON webhook_deliveries (organization_id, event, received_at DESC);
-- The retention sweep deletes deliveries after 30 days.
GRANT DELETE ON webhook_deliveries TO dude_sweeper;

-- What GitHub says the organisation is subscribed to, read at most hourly.
CREATE TABLE forge_event_subscriptions (
  organization_id text PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  source          text NOT NULL CHECK (source IN ('app', 'webhook')),
  events          text[] NOT NULL,
  read_at         timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE forge_event_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE forge_event_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON forge_event_subscriptions USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
