-- The conductor takes the delivery's decisions (design: the conductor,
-- step 3). Who decides is in the workflow's state (state->>'decider'); this
-- is what the decisions need beside it.

-- The phase Runs a task's conductor started (start_phase), by its Run: they
-- sit under it in the task's sessions and Chat, and one of them asking a
-- question wakes it. NULL for the workflow's own. conductor_note is what
-- the conductor asked of the Run, added to its prompt. Nullable with no
-- default, so adding them changes no row; their index is 074's, built
-- without holding this table's lock. The note's bound is NOT VALID: it
-- holds for every write from here, and checking it would read every
-- existing Run (all NULL) under this table's exclusive lock.
ALTER TABLE runs ADD COLUMN conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL;
ALTER TABLE runs ADD COLUMN conductor_note text;
ALTER TABLE runs ADD CONSTRAINT runs_conductor_note_len CHECK (length(conductor_note) <= 4000) NOT VALID;

-- The pull request gate's question: the heads (repository → commit) it was
-- asked at. Its answer opens the pull request only while the task is still
-- at those heads. NULL for every other question.
ALTER TABLE questions ADD COLUMN pr_gate_heads jsonb;

-- A directive the sender took to send to lux, before lux has it (sent_at):
-- claimed, it may be in flight, so withdrawing a wake note's retry leaves
-- it to be heard. Nullable with no default: adding it changes no row.
ALTER TABLE directives ADD COLUMN claimed_at timestamptz;

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
  -- Set when a note carrying it was queued for a conductor, which it names
  -- (the latest); conductor_wake_attempts keeps every note that carried it.
  -- Every attempt failing unheard puts the reason back to pending.
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

-- Each note that carried a reason: the directive queued for a live
-- conductor, or (directive NULL) the briefing a new conductor was started
-- with. Heard: its consumption receipt, or the briefing's prompt read (or
-- accepted with no read receipt to follow); failed: the directive failed,
-- or the conductor ended without hearing its briefing. A reason heard by
-- any attempt is settled, and a retry not yet claimed for sending is
-- withdrawn.
CREATE TABLE conductor_wake_attempts (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  wake_id          text NOT NULL REFERENCES conductor_wakes(id) ON DELETE CASCADE,
  conductor_run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  directive_id     text REFERENCES directives(id) ON DELETE CASCADE,
  created_at       timestamptz NOT NULL DEFAULT now(),
  heard_at         timestamptz,
  failed_at        timestamptz
);
CREATE INDEX conductor_wake_attempts_wake_idx ON conductor_wake_attempts (wake_id);
-- Receipts and failures find a directive's reasons; hand-over asks whether
-- a directive is a wake note.
CREATE INDEX conductor_wake_attempts_directive_idx ON conductor_wake_attempts (directive_id) WHERE directive_id IS NOT NULL;
-- Briefings not yet heard and not failed: the syncer's scan for conductors
-- that ended before hearing theirs.
CREATE INDEX conductor_wake_attempts_briefing_idx ON conductor_wake_attempts (conductor_run_id)
  WHERE directive_id IS NULL AND heard_at IS NULL AND failed_at IS NULL;
-- Briefings not yet heard, failed or not: the prompt's read receipt, which
-- counts after a failure too (BriefingHeardTx).
CREATE INDEX conductor_wake_attempts_briefing_receipt_idx ON conductor_wake_attempts (conductor_run_id)
  WHERE directive_id IS NULL AND heard_at IS NULL;

ALTER TABLE conductor_wake_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE conductor_wake_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conductor_wake_attempts
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON conductor_wake_attempts TO dude_app;
GRANT SELECT ON conductor_wake_attempts TO dude_sweeper;
