-- Phase Runs execute on lux.
--
-- lux owns containers, checkouts, pushes and the agent process. dude keeps
-- what only dude can decide: which phase runs, what it is told, when its work
-- is finished, and what its output means. These columns are dude's side of
-- that boundary — enough to find the lux Run again after a restart and to
-- pick up its output exactly where it was left.

ALTER TABLE runs
  -- The repository this phase works on. The delivery workflow chose it; the
  -- Run carries it so building the lux spec needs nothing from the workflow.
  ADD COLUMN repository_id   text REFERENCES repositories(id) ON DELETE SET NULL,
  -- Which workflow step made this Run. A step that is replayed after a crash
  -- finds the Run its first try made, rather than creating a second.
  ADD COLUMN creation_key    text,
  -- The lux Run executing this one. Unique: one dude Run is one lux Run,
  -- however many times it is paused and resumed.
  ADD COLUMN lux_run_id      text UNIQUE,
  -- The lux Run's state as last observed (submitted, running, stopped, …),
  -- and lux's explanation of it ("exit code 3", "timeout").
  ADD COLUMN lux_state        text,
  ADD COLUMN lux_state_reason text,
  -- Where reading the lux Run's output resumes. Advanced in the same
  -- transaction as the ledger events it produced, so a restart neither
  -- repeats nor skips any of them.
  ADD COLUMN lux_cursor      text,
  ADD COLUMN lux_after_event bigint NOT NULL DEFAULT 0,
  -- Why dude stopped the lux Run: 'complete' when the phase was done,
  -- 'pause' when a person asked. A stop with neither was not dude's doing.
  ADD COLUMN lux_stop_reason text,
  -- Don't try to submit again before this; set after lux refuses for a
  -- reason that may pass (a quota, lux being unreachable).
  ADD COLUMN next_attempt_at timestamptz,

  -- The agent's own session id, which is what lets a resume continue the
  -- conversation rather than start a new one.
  ADD COLUMN agent_session_id text,
  -- The lux placement (epoch) whose agent session has been established.
  -- A resumed agent replays its history before it is ready, and that
  -- replay must not be recorded a second time.
  ADD COLUMN agent_session_epoch integer NOT NULL DEFAULT 0,
  -- What the agent has reported spending so far. Agents report a running
  -- total; the ledger records the increments.
  ADD COLUMN agent_cost_usd  numeric NOT NULL DEFAULT 0,
  -- The agent went busy (took its task) and then idle (finished its turn).
  -- A turn is done only when the second follows the first: an agent is idle
  -- before it has been given anything to do.
  ADD COLUMN agent_busy_at   timestamptz,
  ADD COLUMN turn_done_at    timestamptz,
  -- busy or idle, as the agent last reported it.
  ADD COLUMN agent_activity  text,
  -- Reply text streamed since the last complete message. Kept on the row,
  -- with the cursor, so a restart mid-sentence loses nothing: the message is
  -- recorded whole when the agent calls a tool or ends its turn.
  ADD COLUMN agent_message_buffer text NOT NULL DEFAULT '',

  -- The commit the checkout started from, as lux reported it. A phase with
  -- no base ref starts from the default branch's tip, which moves, so the
  -- sha has to be recorded rather than derived later.
  ADD COLUMN base_sha        text,
  -- The push a publishing phase asked lux for, and what lux reported.
  ADD COLUMN push_request_id text,
  ADD COLUMN push_result     jsonb,
  -- What the phase changed, from the forge's comparison of base and head.
  -- It selects the conditional reviewers and retires findings about files a
  -- fix rewrote.
  ADD COLUMN changed_paths   text[] NOT NULL DEFAULT '{}';

-- The sync sweeper's scan: phase Runs dude still has something to do for.
CREATE INDEX runs_lux_active_idx ON runs (created_at)
  WHERE phase IS NOT NULL
    AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused', 'aborted');

CREATE UNIQUE INDEX runs_creation_key_idx ON runs (work_item_id, creation_key)
  WHERE creation_key IS NOT NULL;

-- A directive is sent to lux, then acknowledged by the agent. `delivered_at`
-- keeps meaning "the agent has it"; `sent_at` stops it being sent twice.
ALTER TABLE directives ADD COLUMN sent_at timestamptz;
-- Whether the directive stops the agent's current turn to be heard now.
-- Without it, an agent that cannot take a message mid-turn (OpenCode, and any
-- ACP agent) hears it when the turn ends — which can be a long time.
ALTER TABLE directives ADD COLUMN interrupt boolean NOT NULL DEFAULT false;

-- ---------------------------------------------------------------------------
-- Two processes, one ledger
-- ---------------------------------------------------------------------------

-- The orchestrator writes most events; the backend feeds the browser. Every
-- insert announces itself, so the backend's live stream sees events from
-- either process. The payload is only what a subscriber filters on — the
-- listener re-reads the row by cursor, because NOTIFY payloads are capped at
-- 8000 bytes and an event payload is not.
CREATE FUNCTION notify_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('dude_events', json_build_object(
    'cursor', NEW.cursor,
    'organizationId', NEW.organization_id,
    'workItemId', NEW.work_item_id,
    'runId', NEW.run_id,
    'sessionId', NEW.session_id)::text);
  RETURN NULL;
END $$;

CREATE TRIGGER events_notify AFTER INSERT ON events
  FOR EACH ROW EXECUTE FUNCTION notify_event();

-- ---------------------------------------------------------------------------
-- GitHub webhooks
-- ---------------------------------------------------------------------------

-- PR observability is by webhook: polling every open PR spends GitHub's rate
-- limit faster than a busy organization can afford.
--
-- The secret GitHub signs deliveries with, one per organization. Generated by
-- dude when the credential is stored; never returned by any route.
ALTER TABLE forge_credentials ADD COLUMN webhook_secret text;

-- Repositories whose webhook dude has registered, so it is registered once.
ALTER TABLE repositories ADD COLUMN webhook_id text;

-- Every delivery, stored before it is acted on. The backend receives and
-- verifies it; the orchestrator processes it. Keyed on GitHub's delivery id,
-- so a redelivery is recognised rather than acted on twice.
CREATE TABLE webhook_deliveries (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  event           text NOT NULL,
  payload         jsonb NOT NULL,
  received_at     timestamptz NOT NULL DEFAULT now(),
  processed_at    timestamptz,
  attempts        integer NOT NULL DEFAULT 0,
  last_error      text
);

CREATE INDEX webhook_deliveries_pending_idx ON webhook_deliveries (received_at)
  WHERE processed_at IS NULL;

ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON webhook_deliveries
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());

GRANT SELECT, INSERT, UPDATE ON webhook_deliveries TO dude_app;
GRANT SELECT, UPDATE ON webhook_deliveries TO dude_sweeper;
GRANT SELECT ON repositories, forge_credentials TO dude_sweeper;

-- The webhook secret, for the one caller that has no tenant yet: a GitHub
-- delivery names its organization in the URL and proves itself with a
-- signature, and the secret to check it with lives behind row-level
-- security. One narrow function rather than an exemption, as for API keys
-- (migration 003).
CREATE FUNCTION webhook_secret_for(p_organization_id text) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp
AS $$
  SELECT webhook_secret FROM forge_credentials
  WHERE organization_id = p_organization_id AND forge = 'github' LIMIT 1
$$;

REVOKE ALL ON FUNCTION webhook_secret_for(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION webhook_secret_for(text) TO dude_app;

-- ---------------------------------------------------------------------------
-- The runner's schema, retired
-- ---------------------------------------------------------------------------

-- dude no longer runs containers: lux does. Its workers, their leases, the
-- containers they ran and where each workspace lived all belonged to the Go
-- runner this migration replaces.
DROP INDEX IF EXISTS runs_pending_control_idx;
DROP INDEX IF EXISTS runs_lease_idx;
DROP INDEX IF EXISTS runs_home_worker_idx;
ALTER TABLE runs
  DROP COLUMN worker_id,
  DROP COLUMN home_worker_id,
  DROP COLUMN workspace_portable,
  DROP COLUMN workspace_path,
  DROP COLUMN lease_expires_at;
DROP TABLE runtime_instances;
DROP TABLE workers;
DROP TYPE runtime_status;
DROP TYPE worker_status;
