-- 262_workflow_dispatches.sql — run_workflow: a conductor's
-- workflow_dispatch and its supervision to the end.
--
-- One row per dispatch. Asked for run details, GitHub answers the dispatch
-- with its run's id, bound at once (state bound); an older GitHub answers
-- with none, and the run is bound afterwards (run_id) from the runs that
-- were not there before the dispatch (prior_runs), and every pass of the supervisor
-- re-reads it and stores what it saw (status, conclusion, attempt,
-- last_seen_at): the decision is always taken from this stored state,
-- never from an event that might have been missed, and a restart resumes
-- from the rows.
--   state         binding (no run bound yet), bound (reading the run),
--                 completed, or unknown (it could not be told which run,
--                 or none came: reason says why); the last two end the
--                 supervision.
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
--   prior_runs    the workflow_dispatch runs of the workflow and ref seen
--                 just before the dispatch: never this dispatch's run;
--                 NULL when that read failed.
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
  state text NOT NULL DEFAULT 'binding' CHECK (state IN ('binding', 'bound', 'completed', 'unknown')),
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
  prior_runs bigint[],
  completed_at timestamptz,
  read_after timestamptz
);
-- A person's answer authorizes one dispatch.
CREATE UNIQUE INDEX workflow_dispatches_question_idx ON workflow_dispatches (question_id) WHERE question_id IS NOT NULL;
-- A run is bound to one dispatch only.
CREATE UNIQUE INDEX workflow_dispatches_run_idx ON workflow_dispatches (repository_id, run_id) WHERE run_id IS NOT NULL;
CREATE INDEX workflow_dispatches_task_idx ON workflow_dispatches (task_id, dispatched_at);
-- The supervisor's pass: the dispatches still read, or owed a wake.
CREATE INDEX workflow_dispatches_live_idx ON workflow_dispatches (last_read_at NULLS FIRST)
  WHERE state IN ('binding', 'bound') OR outcome IS DISTINCT FROM woken_outcome;
ALTER TABLE workflow_dispatches ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_dispatches FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON workflow_dispatches
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON workflow_dispatches TO dude_app;
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
