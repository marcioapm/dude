-- 231_conductor_waits.sql — what the task's conductor waits on (wait_for).
--
-- One open wait per task: a new wait_for ends the open one ('replaced') in
-- the same transaction. The phase syncer evaluates open waits each sweep
-- and, once one holds or its deadline passes, records one wake reason
-- (kind 'wait', key 'wait:<id>') and ends it in one transaction.
--
-- conditions: normalized, [{kind:'runs_ended', runs:[…]},
-- {kind:'pr_checks_final', pr:<pull_requests.id>, head:'…'}].
-- since_cursor: the ledger's cursor at the call; what happened meanwhile
-- is counted from it. call_key: the tool call's id, so a retried call is
-- the same wait.
CREATE TABLE conductor_waits (
  id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  conductor_run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  attempt int NOT NULL,
  conditions jsonb NOT NULL,
  wake_all boolean NOT NULL DEFAULT true,
  note text CHECK (length(note) <= 500),
  deadline_at timestamptz NOT NULL,
  since_cursor bigint NOT NULL,
  call_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  ended_by text CHECK (ended_by IN ('held', 'deadline', 'replaced', 'task_ended', 'conductor_ended')),
  digest text,
  UNIQUE (conductor_run_id, call_key)
);
CREATE UNIQUE INDEX conductor_waits_open_idx ON conductor_waits (task_id) WHERE ended_at IS NULL;
ALTER TABLE conductor_waits ENABLE ROW LEVEL SECURITY;
ALTER TABLE conductor_waits FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conductor_waits
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON conductor_waits TO dude_app;
-- The sweep's cross-tenant probe for open waits.
GRANT SELECT ON conductor_waits TO dude_sweeper;

-- wait: a conductor's wait held, or reached its deadline.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait'));

-- A wait's digest is facts and ids, worst first, up to 2000 characters as
-- a stall's; the other kinds keep their bounds.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 WHEN 'wait' THEN 2000 ELSE 300 END) NOT VALID;
