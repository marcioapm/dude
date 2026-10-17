-- 139_model_calls.sql — dude calls a model directly, and every call is kept.
--
-- The orchestrator sends one Chat Completions request to the LLM proxy for
-- a tier's model (orchestrator/internal/llm Complete), with no agent and no
-- tools. Each call it sends is a row here: who in dude asked, about what,
-- on which tier and model, what was sent (request: the messages and
-- parameters; never the tier's headers, which may carry credentials), what
-- came back, and how long it took. Text is redacted before it is stored.
--
-- The first purpose is a judgement (orchestrator/internal/judge): one
-- question, answered yes, no or unsure with a confidence and a reason,
-- parsed into result. The organisation picks the tier that answers
-- (organizations.judgement_tier_id); NULL is Off, and nothing is asked.

-- The composite keys below make a row naming another organisation's tier
-- or task impossible, whatever the policy lets a transaction write. Tasks
-- are in tasks (work_items before 026).
ALTER TABLE model_tiers ADD UNIQUE (id, organization_id);
ALTER TABLE tasks ADD UNIQUE (id, organization_id);

ALTER TABLE organizations ADD COLUMN judgement_tier_id text,
  ADD FOREIGN KEY (judgement_tier_id, id) REFERENCES model_tiers (id, organization_id) ON DELETE SET NULL (judgement_tier_id);

CREATE TABLE model_calls (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  purpose         text NOT NULL CHECK (purpose IN ('judgement')),
  -- The part of dude that asked, as code names it.
  asker           text NOT NULL CHECK (asker <> '' AND length(asker) <= 100),
  -- A short label for what the call was about: "ABS-28 · abs#5477".
  about           text CHECK (length(about) <= 200),
  task_id         text,
  tier_id         text,
  -- The tier's name and model when the call was sent, so the row reads the
  -- same after the tier is renamed, changed or removed.
  tier_name       text NOT NULL,
  model           text NOT NULL,
  request         jsonb NOT NULL,
  response_text   text,
  result          jsonb,
  status          text NOT NULL CHECK (status IN ('ok', 'error')),
  error           text,
  http_status     integer,
  latency_ms      integer NOT NULL CHECK (latency_ms >= 0),
  input_tokens    integer NOT NULL DEFAULT 0,
  output_tokens   integer NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (task_id, organization_id) REFERENCES tasks (id, organization_id) ON DELETE SET NULL (task_id),
  FOREIGN KEY (tier_id, organization_id) REFERENCES model_tiers (id, organization_id) ON DELETE SET NULL (tier_id)
);
CREATE INDEX model_calls_org_created_idx ON model_calls (organization_id, created_at DESC);

ALTER TABLE model_calls ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_calls FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON model_calls
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON model_calls TO dude_app;
