-- Phase Runs and review findings: the shape of a work item's lifecycle.
--
-- A Run already owns a container, a workspace, a lease and a node affinity —
-- everything a phase needs to be isolated. So a phase is a Run rather than a
-- new concept, and the sequence investigate → implement → review → simplify →
-- test becomes sibling Runs under one Work Item.

CREATE TYPE run_phase AS ENUM (
  'investigate', 'implement', 'review', 'fix', 'simplify', 'test'
);

ALTER TABLE runs
  ADD COLUMN phase         run_phase,
  ADD COLUMN role          agent_role,
  -- The Run this one continues from: a fix Run points at the review that
  -- found the problem, a review Run at the implement Run it is reviewing.
  ADD COLUMN parent_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  -- The ref this Run's workspace is materialized at. NULL means the
  -- repository's default branch.
  ADD COLUMN base_ref      text,
  -- What it produced, for the next phase to build on. NULL for phases that
  -- do not publish.
  ADD COLUMN head_sha      text,
  ADD COLUMN branch        text;

CREATE INDEX runs_parent_idx ON runs (parent_run_id) WHERE parent_run_id IS NOT NULL;

-- `attempt` is a retry of the whole Work Item; `phase` is a step within one
-- attempt. Several Runs now share an attempt, and review fans out, so the old
-- one-run-per-attempt constraint has to go.
ALTER TABLE runs DROP CONSTRAINT runs_work_item_id_attempt_key;

-- ---------------------------------------------------------------------------
-- Review findings
-- ---------------------------------------------------------------------------

-- Severity decides nothing on its own: policy decides what blocks (plan
-- §11.2). A reviewer reports what it found; the workflow applies the rule.
CREATE TYPE finding_severity AS ENUM ('blocking', 'high', 'medium', 'low', 'note');

CREATE TYPE finding_status AS ENUM (
  'open',
  -- A fix Run changed the code and a re-review agrees it is gone.
  'resolved',
  -- The code it described no longer exists, so the finding is moot.
  'superseded',
  -- A human decided to ship it anyway. Deliberate, and recorded as such.
  'accepted'
);

CREATE TABLE review_findings (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  work_item_id    text NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  -- The review Run that reported it. Kept when that Run is deleted: a
  -- finding outlives the review that found it.
  run_id          text REFERENCES runs(id) ON DELETE SET NULL,
  -- Which reviewer flavour found it — correctness, security, performance…
  -- Reviews fan out in parallel, so this is how their findings stay
  -- attributable after they merge into one set.
  category        text NOT NULL,

  severity        finding_severity NOT NULL,
  status          finding_status NOT NULL DEFAULT 'open',

  repo            text,
  file            text,
  line            integer,
  title           text NOT NULL,
  description     text NOT NULL DEFAULT '',
  suggested_fix   text NOT NULL DEFAULT '',

  -- The fix Run that addressed it, and why it left `open` state.
  resolved_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  resolution_note    text NOT NULL DEFAULT '',

  -- How many fix attempts this finding has survived. A finding that outlives
  -- two attempts is not converging, and a third is unlikely to help
  -- (plan §11.3).
  fix_attempts    integer NOT NULL DEFAULT 0,

  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX review_findings_work_item_idx ON review_findings (work_item_id);
-- The loop's hot query: what is still blocking this work item?
CREATE INDEX review_findings_open_idx ON review_findings (work_item_id, severity)
  WHERE status = 'open';

ALTER TABLE review_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_findings FORCE ROW LEVEL SECURITY;

CREATE POLICY review_findings_isolation ON review_findings
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));

GRANT SELECT, INSERT, UPDATE ON review_findings TO dude_app;

-- Set when the phase-notifier has told the waiting workflow this Run
-- finished. Without it the notifier cannot tell a Run it has reported from
-- one it has not, and would re-signal every finished Run on every sweep.
ALTER TABLE runs ADD COLUMN phase_notified_at timestamptz;

-- The notifier's hot path: finished phase Runs nobody has reported yet.
CREATE INDEX runs_phase_unnotified_idx ON runs (ended_at)
  WHERE phase IS NOT NULL AND phase_notified_at IS NULL;

GRANT SELECT, UPDATE ON runs TO dude_sweeper;
GRANT SELECT ON workflow_runs TO dude_sweeper;
