-- The conductor takes the delivery's decisions (design: the conductor,
-- step 3). Who decides is in the workflow's state (state->>'decider'); this
-- is what the decisions need beside it.

-- The phase Runs a task's conductor started (start_phase), by its Run: they
-- sit under it in the task's sessions and Chat, and one of them asking a
-- question wakes it. NULL for the workflow's own. conductor_note is what
-- the conductor asked of the Run, added to its prompt.
ALTER TABLE runs ADD COLUMN conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL;
ALTER TABLE runs ADD COLUMN conductor_note text CHECK (length(conductor_note) <= 4000);
CREATE INDEX runs_conductor_run_idx ON runs (conductor_run_id) WHERE conductor_run_id IS NOT NULL;

-- The pull request gate's question: the heads (repository → commit) it was
-- asked at. Its answer opens the pull request only while the task is still
-- at those heads. NULL for every other question.
ALTER TABLE questions ADD COLUMN pr_gate_heads jsonb;

-- Why a task's conductor is to be woken, one row per line of its note,
-- until a note listing them is queued for it: reasons arriving within the
-- window, or while it is mid-turn, are one turn. A line is fixed-size facts
-- and ids, never a diff, a file list or a finding's text.
CREATE TABLE conductor_wakes (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id          text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  -- decision: the workflow parked on one; escalation: it stopped for a
  -- person; question: a Run the conductor started asks one; safety: the
  -- conductor has been parked long with a Run of its own in flight.
  kind             text NOT NULL CHECK (kind IN ('decision', 'escalation', 'question', 'safety')),
  -- What makes a repeat the same reason (a workflow step replayed, the
  -- safety net's Run): one row per task and key.
  key              text NOT NULL,
  line             text NOT NULL CHECK (length(line) <= 300),
  created_at       timestamptz NOT NULL DEFAULT now(),
  -- Set when a note carrying it was queued for a conductor, which it names.
  delivered_at     timestamptz,
  conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  UNIQUE (task_id, key)
);
CREATE INDEX conductor_wakes_pending_idx ON conductor_wakes (task_id, created_at) WHERE delivered_at IS NULL;

ALTER TABLE conductor_wakes ENABLE ROW LEVEL SECURITY;
ALTER TABLE conductor_wakes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conductor_wakes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON conductor_wakes TO dude_app;
-- The syncer finds the tasks with reasons pending across organizations, and
-- delivers each in its organization's own transaction.
GRANT SELECT ON conductor_wakes TO dude_sweeper;
