-- 233_run_recoveries.sql — recover_run: the committed work of an ended Run,
-- pushed from lux's checkpoint to a branch of its own.
--
-- The conductor asks (status requested); the recovery worker
-- (phases.SettleRecoveries) reads lux's checkpoints of the dead Run's lux
-- Run, submits a short publisher Run cloned from them (submitted), scans
-- its commits for the dead Run's secrets, asks lux to push (asked), and
-- ends it: done, failed, or refused (a secret in it). One per Run and
-- branch: asking again is the same recovery.
--
-- The publisher Run is not a runs row: its lux cost is kept here, read by
-- the cost sweep (phases.Costs) as a Run's is, and counted for the dead
-- Run by run_model_usd, run_metrics and run_cost_origin below.
-- branch_released_at: when the branch was deleted with the task's other
-- per-Run branches, once the task ended (phases.RetireCompleted).
CREATE TABLE run_recoveries (
  id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  branch text NOT NULL,
  note text CHECK (length(note) <= 500),
  status text NOT NULL DEFAULT 'requested'
    CHECK (status IN ('requested', 'submitted', 'asked', 'done', 'failed', 'refused')),
  -- The checkpoints chosen (repo, head, base) and the repositories whose
  -- uncommitted work was not captured.
  checkpoints jsonb,
  dirty text[],
  publisher_lux_run_id text,
  -- Per repository: status, commit, error.
  result jsonb,
  error text,
  claim_token text,
  claimed_until timestamptz,
  next_attempt_at timestamptz,
  failures int NOT NULL DEFAULT 0,
  -- How far the publisher's lux events were read for its git.push, and
  -- since when the current step has waited on lux.
  events_after bigint NOT NULL DEFAULT 0,
  stepped_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  branch_released_at timestamptz,
  -- The publisher's cost, as 060's on runs.
  lux_ai_usd numeric,
  lux_compute_usd numeric,
  lux_cost_status text,
  lux_cost_read_at timestamptz,
  lux_cost_next_at timestamptz,
  UNIQUE (run_id, branch)
);
CREATE INDEX run_recoveries_open_idx ON run_recoveries (next_attempt_at NULLS FIRST, created_at) WHERE ended_at IS NULL;
CREATE INDEX run_recoveries_task_idx ON run_recoveries (task_id);
CREATE INDEX run_recoveries_cost_idx ON run_recoveries (lux_cost_next_at) WHERE lux_cost_next_at IS NOT NULL;
CREATE INDEX run_recoveries_release_idx ON run_recoveries (task_id) WHERE ended_at IS NOT NULL AND branch_released_at IS NULL;
ALTER TABLE run_recoveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_recoveries FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON run_recoveries
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON run_recoveries TO dude_app;
-- The worker, the cost sweep and the branch release find their rows across
-- organizations; the worker claims them.
GRANT SELECT ON run_recoveries TO dude_sweeper;
GRANT UPDATE (claim_token, claimed_until) ON run_recoveries TO dude_sweeper;

-- recovered: a recovery of the conductor's ended, failed or was refused.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered'));
