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
-- the cost sweep (phases.Costs) as a Run's is, summed onto the dead Run's
-- row, and counted for it by run_model_usd, run_metrics and
-- run_cost_origin below.
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
-- A Run's cost counts its recoveries' publishers (061's rule for each half:
-- lux's figure once reported), kept on the dead Run's row so the cost
-- functions stay functions of the row, inlined into a list of every Run:
-- the sums of its recoveries' AI and compute cost, and how many of them
-- have a publisher whose cost is not yet final. Kept by the trigger below
-- in the transaction that stores a recovery's cost or submits its
-- publisher, by difference, so two recoveries of one Run never race.
ALTER TABLE runs ADD COLUMN recovery_ai_usd numeric,
  ADD COLUMN recovery_compute_usd numeric,
  ADD COLUMN recoveries_unsettled int NOT NULL DEFAULT 0;

CREATE FUNCTION count_run_recovery_cost() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  ai numeric := COALESCE(NEW.lux_ai_usd, 0);
  compute numeric := COALESCE(NEW.lux_compute_usd, 0);
  unsettled int := CASE WHEN NEW.publisher_lux_run_id IS NOT NULL AND NEW.lux_cost_status IS DISTINCT FROM 'final' THEN 1 ELSE 0 END;
  priced boolean := NEW.lux_ai_usd IS NOT NULL;
  computed boolean := NEW.lux_compute_usd IS NOT NULL;
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF (NEW.lux_ai_usd, NEW.lux_compute_usd, NEW.lux_cost_status, NEW.publisher_lux_run_id)
       IS NOT DISTINCT FROM (OLD.lux_ai_usd, OLD.lux_compute_usd, OLD.lux_cost_status, OLD.publisher_lux_run_id) THEN
      RETURN NULL;
    END IF;
    ai := ai - COALESCE(OLD.lux_ai_usd, 0);
    compute := compute - COALESCE(OLD.lux_compute_usd, 0);
    unsettled := unsettled - CASE WHEN OLD.publisher_lux_run_id IS NOT NULL AND OLD.lux_cost_status IS DISTINCT FROM 'final' THEN 1 ELSE 0 END;
  END IF;
  IF ai = 0 AND compute = 0 AND unsettled = 0 AND NOT priced AND NOT computed THEN
    RETURN NULL;
  END IF;
  UPDATE runs SET recovery_ai_usd = CASE WHEN priced OR recovery_ai_usd IS NOT NULL THEN COALESCE(recovery_ai_usd, 0) + ai END,
                  recovery_compute_usd = CASE WHEN computed OR recovery_compute_usd IS NOT NULL THEN COALESCE(recovery_compute_usd, 0) + compute END,
                  recoveries_unsettled = recoveries_unsettled + unsettled
  WHERE id = NEW.run_id;
  RETURN NULL;
END $$;
CREATE TRIGGER run_recoveries_cost AFTER INSERT OR UPDATE OF lux_ai_usd, lux_compute_usd, lux_cost_status, publisher_lux_run_id
  ON run_recoveries FOR EACH ROW EXECUTE FUNCTION count_run_recovery_cost();

-- 061's, with the recoveries' AI cost added.
CREATE OR REPLACE FUNCTION run_model_usd(r runs) RETURNS numeric
LANGUAGE sql IMMUTABLE AS $$
  SELECT COALESCE(r.lux_ai_usd, r.agent_cost_usd, 0) + COALESCE(r.recovery_ai_usd, 0)
$$;

-- Settled only once every recovery's cost is too.
CREATE OR REPLACE FUNCTION run_cost_origin(r runs, OUT lux_ai boolean, OUT lux_compute boolean, OUT lux_final boolean)
LANGUAGE sql IMMUTABLE AS $$
  SELECT r.lux_ai_usd IS NOT NULL, r.lux_compute_usd IS NOT NULL,
         r.lux_cost_status IS NOT DISTINCT FROM 'final' AND r.recoveries_unsettled = 0
$$;

-- 061's, with the recoveries' compute added to the machine half.
CREATE OR REPLACE FUNCTION run_metrics(p_run text)
RETURNS TABLE (active_seconds double precision, parked_seconds double precision,
               cost_usd double precision, input_tokens bigint, output_tokens bigint,
               machine_usd double precision)
LANGUAGE sql STABLE AS $$
  WITH r AS (SELECT * FROM runs WHERE id = p_run),
  parks AS (
    SELECT p.occurred_at AS from_at,
           COALESCE((SELECT min(u.occurred_at) FROM events u
                     WHERE u.run_id = p.run_id AND u.event_type = 'run.unparked' AND u.cursor > p.cursor),
                    (SELECT ended_at FROM r), now()) AS to_at
    FROM events p WHERE p.run_id = p_run AND p.event_type = 'run.parked'),
  m AS (
    SELECT
      GREATEST(0, EXTRACT(EPOCH FROM (COALESCE(r.ended_at, now()) - r.started_at))
                  - COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0)) AS active_seconds,
      COALESCE((SELECT sum(EXTRACT(EPOCH FROM (to_at - from_at))) FROM parks), 0) AS parked_seconds,
      run_model_usd(r) AS model_usd, r.lux_compute_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour,
      r.recovery_compute_usd
    FROM r WHERE r.started_at IS NOT NULL
    UNION ALL
    SELECT 0, 0, run_model_usd(r), r.lux_compute_usd, r.input_tokens, r.output_tokens, r.machine_usd_per_hour,
           r.recovery_compute_usd
    FROM r WHERE r.started_at IS NULL)
  SELECT active_seconds::double precision, parked_seconds::double precision,
         model_usd::double precision, input_tokens, output_tokens,
         (COALESCE(lux_compute_usd, active_seconds / 3600 * COALESCE(machine_usd_per_hour, 0))
          + COALESCE(recovery_compute_usd, 0))::double precision
  FROM m
$$;
