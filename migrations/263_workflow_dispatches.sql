-- 263_workflow_dispatches.sql — run_workflow: a conductor's
-- workflow_dispatch and its supervision to the end.
--
-- One row per dispatch, reserved (dispatching) before the dispatch is
-- sent, so one person's answer (question_id) cannot authorize two. Asked
-- for run details, GitHub answers the dispatch with its run's id, bound at
-- once (state bound); an older GitHub answers with none, and the dispatch
-- is unknown: dude never guesses its run. Every pass of the supervisor
-- re-reads a bound run and stores what it saw (status, conclusion,
-- attempt, last_seen_at): the decision is always taken from this stored
-- state, never from an event that might have been missed, and a restart
-- resumes from the rows.
--   state         dispatching (reserved; GitHub not yet answered), bound
--                 (reading the run), completed, unknown (no run id, or
--                 dude never heard GitHub's answer: reason says why), or
--                 failed (GitHub refused the dispatch: no workflow ran,
--                 and the answer is not spent).
--   failed_reads  consecutive reads that failed (auth, 5xx, rate limit):
--                 "cannot tell", never "not yet"; last_error is the latest.
--   outcome       the terminal outcome the conductor is owed a wake for:
--                 completed, unknown, or cannot_see (failed_reads at its
--                 bound; cleared by the next read that succeeds).
--   woken_outcome the outcome that reached the conductor, woken_at when:
--                 by its own wake, or by the held digest of a wait naming
--                 it (woken_wait, that wait). At most one per outcome,
--                 across restarts and passes; never set while it is only
--                 owed.
--   completed_at  when the bound run was first seen completed: it is still
--                 read for a while after, so a re-run (attempt n+1)
--                 reopens it.
--   read_after    GitHub's Retry-After: no read before it.
CREATE TABLE workflow_dispatches (
  id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  repository_id text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  workflow text NOT NULL CHECK (length(workflow) BETWEEN 1 AND 200),
  ref text NOT NULL CHECK (length(ref) BETWEEN 1 AND 255),
  inputs jsonb NOT NULL DEFAULT '{}'::jsonb,
  dispatched_by text REFERENCES runs(id) ON DELETE SET NULL,
  question_id text REFERENCES questions(id) ON DELETE SET NULL,
  dispatched_at timestamptz NOT NULL DEFAULT now(),
  state text NOT NULL DEFAULT 'dispatching' CHECK (state IN ('dispatching', 'bound', 'completed', 'unknown', 'failed')),
  reason text CHECK (length(reason) <= 1000),
  run_id bigint,
  run_attempt int,
  run_status text,
  run_conclusion text,
  run_url text,
  last_seen_at timestamptz,
  last_read_at timestamptz,
  failed_reads int NOT NULL DEFAULT 0,
  failing_since timestamptz,
  last_error text CHECK (length(last_error) <= 1000),
  outcome text CHECK (outcome IN ('completed', 'unknown', 'cannot_see')),
  woken_outcome text CHECK (woken_outcome IN ('completed', 'unknown', 'cannot_see')),
  woken_at timestamptz,
  woken_wait text,
  completed_at timestamptz,
  read_after timestamptz
);
-- A person's answer authorizes one dispatch: reserved before it is sent,
-- so a second call is refused before any POST. A dispatch GitHub refused
-- (failed) ran nothing and does not spend it.
CREATE UNIQUE INDEX workflow_dispatches_question_idx ON workflow_dispatches (question_id)
  WHERE question_id IS NOT NULL AND state <> 'failed';
-- A run is bound to one dispatch only.
CREATE UNIQUE INDEX workflow_dispatches_run_idx ON workflow_dispatches (repository_id, run_id) WHERE run_id IS NOT NULL;
CREATE INDEX workflow_dispatches_task_idx ON workflow_dispatches (task_id, dispatched_at);
-- The supervisor's pass: the dispatches still read, or owed a wake.
CREATE INDEX workflow_dispatches_live_idx ON workflow_dispatches (last_read_at NULLS FIRST)
  WHERE state IN ('dispatching', 'bound') OR outcome IS DISTINCT FROM woken_outcome;
ALTER TABLE workflow_dispatches ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_dispatches FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON workflow_dispatches
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
-- DELETE: a reservation whose call sent nothing (refused first) goes.
GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_dispatches TO dude_app;
-- The supervisor's cross-tenant probe for dispatches to read.
GRANT SELECT ON workflow_dispatches TO dude_sweeper;

-- workflow: a dispatched workflow's terminal outcome (completed, unknown,
-- cannot see), its line the run's facts and the read's error, up to 600
-- characters. Every other kind's bound as 258 left it.
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; both checks only
-- widen the ones they replace.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'workflow')) NOT VALID;
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 WHEN 'round' THEN 2000
    WHEN 'checks' THEN 2000 WHEN 'recovered' THEN 600 WHEN 'workflow' THEN 600 ELSE 300 END) NOT VALID;
