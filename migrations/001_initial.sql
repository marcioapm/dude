-- 001_initial.sql — multi-tenant core schema.
--
-- Tenancy rule: every tenant-scoped table carries organization_id and is
-- protected by row-level security. The application connects as a non-superuser
-- role and sets `app.organization_id` per transaction; RLS then makes a missing
-- or wrong org filter a no-rows result rather than a data leak (plan §53).

-- ---------------------------------------------------------------------------
-- Extensions
-- ---------------------------------------------------------------------------

-- Case-insensitive email comparison without LOWER() on every lookup.
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------------------------------------------------------------------------
-- Helpers
-- ---------------------------------------------------------------------------

-- Current tenant for this transaction. Returns NULL when unset, which makes
-- every RLS predicate below fail closed.
CREATE OR REPLACE FUNCTION current_organization_id() RETURNS text
  LANGUAGE sql STABLE
  AS $$ SELECT NULLIF(current_setting('app.organization_id', true), '') $$;

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
  LANGUAGE plpgsql
  AS $$ BEGIN NEW.updated_at = now(); RETURN NEW; END $$;

-- ---------------------------------------------------------------------------
-- Organizations, users, membership
-- ---------------------------------------------------------------------------

CREATE TABLE organizations (
  id                   text PRIMARY KEY,
  name                 text NOT NULL,
  slug                 text NOT NULL UNIQUE,
  -- Org-wide fallback agent model config: { role -> AgentModelConfig }.
  default_agent_models jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Users are global identities; membership grants access to an organization.
CREATE TABLE users (
  id         text PRIMARY KEY,
  email      citext NOT NULL UNIQUE,
  name       text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TYPE org_role AS ENUM ('owner', 'admin', 'member', 'viewer');

CREATE TABLE org_memberships (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id         text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role            org_role NOT NULL DEFAULT 'member',
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (organization_id, user_id)
);

CREATE INDEX org_memberships_user_idx ON org_memberships (user_id);

-- API keys authenticate machine callers (runner, CLI, E2E suite).
CREATE TABLE api_keys (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            text NOT NULL,
  key_hash        text NOT NULL UNIQUE,
  key_prefix      text NOT NULL,
  -- 'user' keys act for a person; 'runner' keys may only use runner endpoints.
  kind            text NOT NULL DEFAULT 'user' CHECK (kind IN ('user', 'runner')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_used_at    timestamptz,
  revoked_at      timestamptz
);

CREATE INDEX api_keys_org_idx ON api_keys (organization_id) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Projects and repositories
-- ---------------------------------------------------------------------------

CREATE TABLE projects (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            text NOT NULL,
  slug            text NOT NULL,
  description     text NOT NULL DEFAULT '',
  -- Per-role model selection for this project: { role -> AgentModelConfig }.
  -- Roles absent here fall back to organizations.default_agent_models.
  agent_models    jsonb NOT NULL DEFAULT '{}'::jsonb,
  runtime_image   text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, slug)
);

CREATE TYPE trust_class AS ENUM ('trusted_internal', 'untrusted_external');

CREATE TABLE repositories (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name            text NOT NULL,
  url             text NOT NULL,
  default_branch  text NOT NULL DEFAULT 'main',
  trust           trust_class NOT NULL DEFAULT 'trusted_internal',
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, name)
);

CREATE INDEX repositories_org_idx ON repositories (organization_id);

-- ---------------------------------------------------------------------------
-- Epics and work items
-- ---------------------------------------------------------------------------

CREATE TABLE epics (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  title           text NOT NULL,
  description     text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX epics_project_idx ON epics (project_id);

CREATE TYPE work_item_status AS ENUM (
  'received', 'intake', 'awaiting_confirmation', 'queued', 'running',
  'awaiting_human', 'review', 'ready_to_merge', 'done', 'failed', 'aborted'
);

CREATE TABLE work_items (
  id                  text PRIMARY KEY,
  organization_id     text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id          text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  epic_id             text REFERENCES epics(id) ON DELETE SET NULL,
  title               text NOT NULL,
  goal                text NOT NULL DEFAULT '',
  acceptance_criteria jsonb NOT NULL DEFAULT '[]'::jsonb,
  status              work_item_status NOT NULL DEFAULT 'received',
  requested_by        text REFERENCES users(id) ON DELETE SET NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX work_items_project_status_idx ON work_items (project_id, status);
CREATE INDEX work_items_org_created_idx ON work_items (organization_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Workers (execution plane capacity)
-- ---------------------------------------------------------------------------

CREATE TYPE worker_status AS ENUM ('registering', 'ready', 'draining', 'lost');

-- Workers may be shared infrastructure, so organization_id is nullable and
-- this table is deliberately NOT under RLS; it is control-plane operational
-- state, exposed to tenants only through filtered API responses.
CREATE TABLE workers (
  id                  text PRIMARY KEY,
  organization_id     text REFERENCES organizations(id) ON DELETE CASCADE,
  name                text NOT NULL,
  pool                text NOT NULL DEFAULT 'local',
  status              worker_status NOT NULL DEFAULT 'registering',
  cpu_millis          integer NOT NULL DEFAULT 0,
  memory_mb           integer NOT NULL DEFAULT 0,
  max_runs            integer NOT NULL DEFAULT 1,
  active_runs         integer NOT NULL DEFAULT 0,
  cached_images       jsonb NOT NULL DEFAULT '[]'::jsonb,
  cached_repositories jsonb NOT NULL DEFAULT '[]'::jsonb,
  registered_at       timestamptz NOT NULL DEFAULT now(),
  last_heartbeat_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX workers_pool_status_idx ON workers (pool, status);
CREATE INDEX workers_heartbeat_idx ON workers (last_heartbeat_at) WHERE status = 'ready';

-- ---------------------------------------------------------------------------
-- Runs, runtime instances, sessions
-- ---------------------------------------------------------------------------

CREATE TYPE run_status AS ENUM (
  'pending', 'scheduled', 'starting', 'running', 'paused',
  'completed', 'failed', 'aborted'
);

CREATE TABLE runs (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  work_item_id    text NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  attempt         integer NOT NULL CHECK (attempt > 0),
  status          run_status NOT NULL DEFAULT 'pending',
  worker_id       text REFERENCES workers(id) ON DELETE SET NULL,
  workspace_path  text,
  -- Lease held by the worker currently executing this Run. Expiry lets the
  -- control plane reclaim work from a worker that disappeared (plan §32).
  lease_expires_at timestamptz,
  error           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  started_at      timestamptz,
  ended_at        timestamptz,
  UNIQUE (work_item_id, attempt)
);

CREATE INDEX runs_status_idx ON runs (status);
CREATE INDEX runs_work_item_idx ON runs (work_item_id);
-- Scheduler hot path: unassigned runs waiting for capacity.
CREATE INDEX runs_schedulable_idx ON runs (created_at) WHERE status = 'pending';
-- Reaper hot path: leases that may have expired.
CREATE INDEX runs_lease_idx ON runs (lease_expires_at) WHERE lease_expires_at IS NOT NULL;

CREATE TYPE runtime_status AS ENUM (
  'creating', 'starting', 'running', 'stopping', 'stopped', 'destroyed', 'failed'
);

CREATE TABLE runtime_instances (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  worker_id       text NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
  container_id    text,
  image_digest    text,
  -- Increments when a container is replaced for the same Run; the Run and
  -- Session identities survive the container (plan §61).
  generation      integer NOT NULL DEFAULT 1,
  status          runtime_status NOT NULL DEFAULT 'creating',
  created_at      timestamptz NOT NULL DEFAULT now(),
  started_at      timestamptz,
  stopped_at      timestamptz,
  destroyed_at    timestamptz,
  UNIQUE (run_id, generation)
);

CREATE INDEX runtime_instances_run_idx ON runtime_instances (run_id);
CREATE INDEX runtime_instances_live_idx ON runtime_instances (worker_id)
  WHERE status IN ('creating', 'starting', 'running');

CREATE TYPE agent_role AS ENUM (
  'orchestrator', 'investigator', 'implementer', 'reviewer', 'simplifier', 'qa_browser'
);

CREATE TYPE session_status AS ENUM (
  'pending', 'running', 'waiting_on_human', 'completed', 'failed', 'aborted'
);

CREATE TABLE sessions (
  id                  text PRIMARY KEY,
  organization_id     text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id              text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  parent_session_id   text REFERENCES sessions(id) ON DELETE CASCADE,
  role                agent_role NOT NULL,
  harness             text NOT NULL,
  model               text NOT NULL,
  status              session_status NOT NULL DEFAULT 'pending',
  -- Harness-native ID kept beside our stable ID (plan §31).
  external_session_id text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  ended_at            timestamptz
);

CREATE INDEX sessions_run_idx ON sessions (run_id);
CREATE INDEX sessions_parent_idx ON sessions (parent_session_id);
CREATE INDEX sessions_external_idx ON sessions (external_session_id)
  WHERE external_session_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Event ledger
-- ---------------------------------------------------------------------------

-- Append-only. The authoritative record of what happened and when; current
-- state lives in the tables above (plan §4.2). Never updated, never deleted
-- except by retention policy.
CREATE TABLE events (
  -- Global monotonic cursor. bigint identity rather than a timestamp so
  -- resume-after-cursor is exact and never skips a concurrently-committed row.
  cursor          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  id              text NOT NULL UNIQUE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  event_type      text NOT NULL,
  occurred_at     timestamptz NOT NULL DEFAULT now(),

  project_id      text,
  work_item_id    text,
  run_id          text,
  session_id      text,
  workflow_run_id text,

  actor_type      text NOT NULL CHECK (actor_type IN ('system', 'human', 'agent', 'integration')),
  actor_id        text NOT NULL,
  source          text NOT NULL,

  correlation_id  text,
  causation_id    text,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- Primary read paths: a Session's timeline, a Run's timeline, an org feed.
CREATE INDEX events_session_cursor_idx ON events (session_id, cursor) WHERE session_id IS NOT NULL;
CREATE INDEX events_run_cursor_idx ON events (run_id, cursor) WHERE run_id IS NOT NULL;
CREATE INDEX events_work_item_cursor_idx ON events (work_item_id, cursor) WHERE work_item_id IS NOT NULL;
CREATE INDEX events_org_cursor_idx ON events (organization_id, cursor DESC);
CREATE INDEX events_type_idx ON events (organization_id, event_type, cursor DESC);
CREATE INDEX events_correlation_idx ON events (correlation_id) WHERE correlation_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Durable workflow runtime (plan §21 Option A)
-- ---------------------------------------------------------------------------

CREATE TYPE workflow_run_status AS ENUM (
  'running', 'waiting', 'completed', 'failed', 'dead_lettered', 'aborted'
);

CREATE TABLE workflow_runs (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  workflow_type   text NOT NULL,
  -- Deduplicates starts; a repeat start returns the original run.
  idempotency_key text NOT NULL,
  status          workflow_run_status NOT NULL DEFAULT 'running',
  step            text NOT NULL,
  state           jsonb NOT NULL DEFAULT '{}'::jsonb,
  attempt         integer NOT NULL DEFAULT 0,
  last_error      text,
  -- Parked until this time; NULL with status 'running' means runnable now.
  wake_at         timestamptz,
  -- Signal names this run is parked on; empty means it is not signal-waiting.
  awaiting_signals jsonb NOT NULL DEFAULT '[]'::jsonb,
  work_item_id    text REFERENCES work_items(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE CASCADE,
  -- Worker lease, so exactly one poller advances a run at a time.
  locked_by       text,
  locked_until    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_id, workflow_type, idempotency_key)
);

-- The poller's claim query: runnable rows, oldest first.
CREATE INDEX workflow_runs_runnable_idx ON workflow_runs (wake_at NULLS FIRST, created_at)
  WHERE status IN ('running', 'waiting');
CREATE INDEX workflow_runs_work_item_idx ON workflow_runs (work_item_id) WHERE work_item_id IS NOT NULL;

-- Signal inbox. Signals are durable and may arrive before the workflow parks.
CREATE TABLE workflow_signals (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  workflow_run_id text NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
  name            text NOT NULL,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- Dedupes redelivered webhooks (plan §26).
  idempotency_key text,
  received_at     timestamptz NOT NULL DEFAULT now(),
  consumed_at     timestamptz
);

CREATE UNIQUE INDEX workflow_signals_idem_idx
  ON workflow_signals (workflow_run_id, idempotency_key)
  WHERE idempotency_key IS NOT NULL;
CREATE INDEX workflow_signals_pending_idx ON workflow_signals (workflow_run_id, received_at)
  WHERE consumed_at IS NULL;

-- Transactional outbox: side effects are committed with the state change
-- that caused them, then dispatched at-least-once by a separate poller.
CREATE TABLE workflow_outbox (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  workflow_run_id text REFERENCES workflow_runs(id) ON DELETE CASCADE,
  kind            text NOT NULL,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  attempts        integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  dispatched_at   timestamptz,
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX workflow_outbox_pending_idx ON workflow_outbox (next_attempt_at)
  WHERE dispatched_at IS NULL;

-- ---------------------------------------------------------------------------
-- Human questions (plan §8)
-- ---------------------------------------------------------------------------

CREATE TYPE question_status AS ENUM ('open', 'answered', 'cancelled', 'expired');

CREATE TABLE questions (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  work_item_id    text REFERENCES work_items(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  -- Blocking questions park the workflow; non-blocking ones do not.
  blocking        boolean NOT NULL DEFAULT true,
  prompt          text NOT NULL,
  -- Optional multiple-choice options; free text when empty.
  options         jsonb NOT NULL DEFAULT '[]'::jsonb,
  status          question_status NOT NULL DEFAULT 'open',
  answer          text,
  answered_by     text REFERENCES users(id) ON DELETE SET NULL,
  asked_at        timestamptz NOT NULL DEFAULT now(),
  answered_at     timestamptz,
  expires_at      timestamptz
);

CREATE INDEX questions_open_idx ON questions (organization_id, asked_at DESC) WHERE status = 'open';
CREATE INDEX questions_run_idx ON questions (run_id);

-- ---------------------------------------------------------------------------
-- Artifacts (plan §40, §66)
-- ---------------------------------------------------------------------------

CREATE TABLE artifacts (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  kind            text NOT NULL,
  name            text NOT NULL,
  content_type    text NOT NULL DEFAULT 'application/octet-stream',
  size_bytes      bigint NOT NULL DEFAULT 0,
  -- Storage key only; never a credentialed URL (plan §66).
  storage_key     text NOT NULL,
  sha256          text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX artifacts_run_idx ON artifacts (run_id);
CREATE INDEX artifacts_session_idx ON artifacts (session_id);

-- ---------------------------------------------------------------------------
-- Cost accounting (plan §19)
-- ---------------------------------------------------------------------------

CREATE TABLE cost_samples (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text REFERENCES projects(id) ON DELETE CASCADE,
  work_item_id    text REFERENCES work_items(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  role            agent_role,
  model           text NOT NULL,
  input_tokens    bigint NOT NULL DEFAULT 0,
  output_tokens   bigint NOT NULL DEFAULT 0,
  cached_tokens   bigint NOT NULL DEFAULT 0,
  cost_usd        numeric(12, 6) NOT NULL DEFAULT 0,
  occurred_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX cost_samples_run_idx ON cost_samples (run_id);
CREATE INDEX cost_samples_org_time_idx ON cost_samples (organization_id, occurred_at DESC);

-- ---------------------------------------------------------------------------
-- updated_at triggers
-- ---------------------------------------------------------------------------

CREATE TRIGGER organizations_updated_at BEFORE UPDATE ON organizations
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER projects_updated_at BEFORE UPDATE ON projects
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER epics_updated_at BEFORE UPDATE ON epics
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER work_items_updated_at BEFORE UPDATE ON work_items
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER workflow_runs_updated_at BEFORE UPDATE ON workflow_runs
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- Row-level security
-- ---------------------------------------------------------------------------

-- Every tenant-scoped table gets the same predicate. FORCE applies it to the
-- table owner too, so even the migration role cannot accidentally cross
-- tenants at runtime.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'projects', 'repositories', 'epics', 'work_items', 'runs',
    'runtime_instances', 'sessions', 'events', 'workflow_runs',
    'workflow_signals', 'workflow_outbox', 'questions', 'artifacts',
    'cost_samples', 'api_keys'
  ]
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I USING (organization_id = current_organization_id())'
      ' WITH CHECK (organization_id = current_organization_id())', t);
  END LOOP;
END $$;
