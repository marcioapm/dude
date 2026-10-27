-- 211_incoming_webhooks.sql — an automation's own URL: POST /v1/hooks/<hook id>.
--
-- A third trigger kind, {kind: incoming, hookId, verify, conditions[],
-- groupBy, ratePerHour}. Every request to the URL is stored before anything
-- acts on it, as a GitHub delivery is (webhook_deliveries, 183): accepted
-- ones with their body, for the automations loop to match; refused ones
-- (bad secret, too large, not JSON, over the rate, turned off) with their
-- reason and no body. docs/CONTRIBUTING.md, "Automations"; docs/operations.md,
-- "Incoming webhooks".

ALTER TABLE automations DROP CONSTRAINT automations_trigger_check;
ALTER TABLE automations ADD CONSTRAINT automations_trigger_check
  CHECK (jsonb_typeof(trigger) = 'object' AND trigger->>'kind' IN ('schedule', 'github', 'incoming'));
-- A server answers GitHub events (183's automations_server_events) whatever
-- else is added; an incoming trigger names its hook.
ALTER TABLE automations ADD CONSTRAINT automations_incoming_hook
  CHECK (trigger->>'kind' <> 'incoming' OR trigger->>'hookId' ~ '^hk_[a-z0-9]{12,40}$');

-- The verification secret: kept like webhook_secret (never read back by the
-- API, which answers its last four characters).
ALTER TABLE automations ADD COLUMN incoming_secret text, ADD COLUMN incoming_hint text;
-- An organisation incoming automation's projects: create_task files there and
-- their repositories are the ones it may check out (at least one; the API says so).
ALTER TABLE automations ADD COLUMN project_ids text[] NOT NULL DEFAULT '{}';
ALTER TABLE automations ADD CONSTRAINT automations_project_ids_org_incoming
  CHECK (cardinality(project_ids) = 0 OR (project_id IS NULL AND trigger->>'kind' = 'incoming'));
-- The public route finds a hook before any tenant scope exists.
CREATE UNIQUE INDEX automations_hook_idx ON automations ((trigger->>'hookId')) WHERE trigger->>'kind' = 'incoming';

-- What the public route needs of a hook, around row-level security: the one
-- read before an organisation is known. Nothing else of the automation.
CREATE FUNCTION incoming_hook(p_hook text)
RETURNS TABLE (automation_id text, organization_id text, enabled boolean, verify jsonb, secret text, rate_per_hour integer)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT a.id, a.organization_id, a.enabled, a.trigger->'verify', a.incoming_secret,
    COALESCE((a.trigger->>'ratePerHour')::integer, 30)
  FROM automations a WHERE a.trigger->>'kind' = 'incoming' AND a.trigger->>'hookId' = p_hook
$$;
REVOKE ALL ON FUNCTION incoming_hook(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION incoming_hook(text) TO dude_app;

-- One request to an incoming automation's URL.
CREATE TABLE incoming_deliveries (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  automation_id   text NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
  received_at     timestamptz NOT NULL DEFAULT now(),
  -- NULL until the automations loop reads an accepted one; refused at once.
  verdict         text CHECK (verdict IN ('started', 'sent_to_run', 'skipped', 'refused')),
  -- A refusal's code; a skip's missed condition (reason_index) in words.
  reason          text CHECK (length(reason) <= 2000),
  reason_index    integer,
  group_key       text CHECK (length(group_key) <= 2000),
  bytes           integer NOT NULL CHECK (bytes >= 0),
  -- As received, with the values of Authorization, the signature header,
  -- cookies and any *token* / *secret* name dropped (the control plane does it).
  headers         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(headers) = 'object'),
  -- The body: never kept for a refused request, emptied after 30 days.
  payload         jsonb,
  payload_purged_at timestamptz,
  run_id          text REFERENCES automation_runs(id) ON DELETE SET NULL,
  -- A test of an automation with a written body: kept for the run, never counted.
  test            boolean NOT NULL DEFAULT false,
  UNIQUE (organization_id, id),
  CONSTRAINT incoming_deliveries_refused_bodyless CHECK (verdict IS DISTINCT FROM 'refused' OR payload IS NULL),
  CONSTRAINT incoming_deliveries_refusal_code CHECK (verdict IS DISTINCT FROM 'refused'
    OR reason IN ('bad_signature', 'not_json', 'too_large', 'rate_limited', 'disabled'))
);
ALTER TABLE incoming_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE incoming_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON incoming_deliveries USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
-- The Deliveries tab, newest first; and the rate limit's rolling hour.
CREATE INDEX incoming_deliveries_automation_idx ON incoming_deliveries (automation_id, received_at DESC);
-- The loop's queue: accepted, not yet read.
CREATE INDEX incoming_deliveries_pending_idx ON incoming_deliveries (received_at) WHERE verdict IS NULL;
-- Retention: refused rows go at 7 days, bodies at 30.
CREATE INDEX incoming_deliveries_refused_idx ON incoming_deliveries (received_at) WHERE verdict = 'refused';
CREATE INDEX incoming_deliveries_payload_kept_idx ON incoming_deliveries (received_at) WHERE payload IS NOT NULL;
-- Run detail's "Sent to this run".
CREATE INDEX incoming_deliveries_run_idx ON incoming_deliveries (run_id) WHERE run_id IS NOT NULL;

-- The matcher, the runner and retention work across organisations.
GRANT SELECT, UPDATE, DELETE ON incoming_deliveries TO dude_sweeper;

-- A run's payloads gain the delivery's group key ({deliveryId, event:
-- "incoming", groupKey, body}); a live run's group is its first payload's.
CREATE INDEX automation_runs_group_idx ON automation_runs (automation_id, (payloads->0->>'groupKey'))
  WHERE status IN ('queued', 'running');
