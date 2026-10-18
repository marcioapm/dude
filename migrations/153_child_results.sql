-- 153_child_results.sql
ALTER TABLE runs
  ADD COLUMN child_generation integer NOT NULL DEFAULT 1 CHECK (child_generation > 0),
  ADD COLUMN child_replaces text REFERENCES runs(id),
  ADD COLUMN child_restart_key text,
  ADD COLUMN child_stop_reason text CHECK (child_stop_reason IN ('aborted', 'restarted', 'parent-ended'));

CREATE TABLE child_results (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  parent_run_id text NOT NULL REFERENCES runs(id),
  child_run_id text NOT NULL REFERENCES runs(id),
  generation integer NOT NULL CHECK (generation > 0),
  source_kind text NOT NULL CHECK (source_kind = 'task_conductor'),
  profile text NOT NULL,
  status text NOT NULL CHECK (status IN ('completed', 'failed', 'aborted')),
  findings integer NOT NULL CHECK (findings >= 0),
  verdicts integer NOT NULL CHECK (verdicts >= 0),
  commits jsonb NOT NULL,
  artifact_ids text[] NOT NULL,
  cost_usd double precision NOT NULL CHECK (cost_usd >= 0 AND cost_usd < 'Infinity'::double precision),
  stale boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (parent_run_id, child_run_id, generation)
);

CREATE TABLE child_result_outbox (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  parent_run_id text NOT NULL,
  child_run_id text NOT NULL,
  generation integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  consumed_at timestamptz,
  stale_at timestamptz,
  PRIMARY KEY (parent_run_id, child_run_id, generation),
  FOREIGN KEY (parent_run_id, child_run_id, generation)
    REFERENCES child_results(parent_run_id, child_run_id, generation)
);

ALTER TABLE child_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE child_results FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON child_results
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
ALTER TABLE child_result_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE child_result_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON child_result_outbox
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON child_results, child_result_outbox TO dude_app;
