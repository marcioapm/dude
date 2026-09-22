-- 009_interventions.sql — steer, pause, resume, abort (plan §24).
--
-- These are workflow signals and durable domain objects, not chat messages.
-- A human redirecting an agent must be as auditable as the agent's own
-- actions: who asked for what, when, and whether it took effect.

-- ---------------------------------------------------------------------------
-- Directives
-- ---------------------------------------------------------------------------

-- A steering instruction, versioned so a later directive can supersede an
-- earlier one without erasing it (plan §24). The history is the point: "why
-- did it do that?" is often answered by a directive issued twenty minutes ago.
CREATE TABLE directives (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  work_item_id    text REFERENCES work_items(id) ON DELETE CASCADE,
  run_id          text REFERENCES runs(id) ON DELETE CASCADE,

  text            text NOT NULL,
  created_by      text REFERENCES users(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),

  -- 'run' applies for the rest of this Run; 'turn' applies to the current
  -- agent turn only. Scope is what makes "stop doing X" different from
  -- "for this one step, do Y".
  scope           text NOT NULL DEFAULT 'run' CHECK (scope IN ('run', 'turn')),

  -- A directive that replaces an earlier one. The earlier row stays.
  supersedes      text REFERENCES directives(id) ON DELETE SET NULL,

  -- When the agent actually received it. NULL means queued: either the
  -- session is mid-turn, or the Run has not started yet.
  delivered_at    timestamptz
);

CREATE INDEX directives_run_idx ON directives (run_id, created_at);
-- The runner's poll: undelivered directives for a live Run.
CREATE INDEX directives_pending_idx ON directives (run_id)
  WHERE delivered_at IS NULL;

-- ---------------------------------------------------------------------------
-- Run control
-- ---------------------------------------------------------------------------

-- What a human has asked of a Run, independent of what it is currently doing.
-- Kept separate from `run_status` because they answer different questions:
-- status says what the Run *is*, control says what was *requested*. A Run can
-- be 'running' with a pending 'pause' for the moment between the two.
CREATE TYPE run_control AS ENUM ('none', 'pause_graceful', 'pause_hard', 'abort');

ALTER TABLE runs
  -- The pending request. Cleared once the runner has acted on it.
  ADD COLUMN control run_control NOT NULL DEFAULT 'none',
  ADD COLUMN control_requested_at timestamptz,
  ADD COLUMN control_requested_by text REFERENCES users(id) ON DELETE SET NULL,
  -- Why the human asked, so the ledger explains the interruption.
  ADD COLUMN control_reason text;

-- The runner polls for Runs it holds that have a pending control request.
CREATE INDEX runs_pending_control_idx ON runs (worker_id)
  WHERE control <> 'none';

-- 'paused' already exists in run_status but nothing ever wrote it; pause now
-- does. Resume returns the Run to 'pending' so it is re-claimable, since the
-- plan is explicit that resume must not assume the prior process survived.

ALTER TABLE directives ENABLE ROW LEVEL SECURITY;
ALTER TABLE directives FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON directives
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON directives TO dude_app;
-- The run-lease reaper needs to see control state when reclaiming.
GRANT SELECT, UPDATE ON directives TO dude_sweeper;
