-- 268_pr_held_actions.sql — a fixer's replies and thread resolutions wait
-- for its fix to be on the pull request (#485).
--
-- A fixer answering pull request feedback (phase fix) may reply on the
-- pull request and resolve its threads during its turn, before its
-- commits pass the checks gate and are pushed. Each such call is checked
-- as before and then held here, with what to post (action: the checked
-- reply or resolution, as delivery wrote it). Once the Run completes —
-- its push published to the pull request's branch, so the head holds its
-- commits — the sweep posts its held rows in id order, once each
-- (posted_at claimed before GitHub is called). A Run that ends any other
-- way drops them (dropped_at), and its conductor is told once
-- (conductor_wakes kind held_dropped).
CREATE TABLE pr_held_actions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('reply', 'resolve_thread')),
  action jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  posted_at timestamptz,
  dropped_at timestamptz
);
-- The sweep's and a Run's end's read: those still held.
CREATE INDEX pr_held_actions_held_idx ON pr_held_actions (run_id, id) WHERE posted_at IS NULL AND dropped_at IS NULL;
ALTER TABLE pr_held_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE pr_held_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pr_held_actions
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON pr_held_actions TO dude_app;
-- The sweep's cross-tenant probe for held actions whose Run completed.
GRANT SELECT ON pr_held_actions TO dude_sweeper;

-- held_dropped: a fixer's held replies and resolutions dropped with its
-- Run. NOT VALID: no scan under ACCESS EXCLUSIVE; every kind 265 allows
-- is still allowed.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'workflow', 'epic_merge', 'held_dropped')) NOT VALID;
