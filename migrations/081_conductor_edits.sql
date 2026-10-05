-- 081_conductor_edits.sql — the conductor edits code, for small things.

-- Its checkout, kept current: each wake of a running conductor, and each
-- resume, syncs its writable repositories to the task branch in lux's
-- fast-forward mode. checkout_sync_id is the sync in flight (a POST /sync
-- request id, or 'resume:<epoch>' for a resume's), checkout_sync_repos the
-- repositories it has not reported yet, checkout_sync_at when it was
-- asked; checkout_synced_at when the last one was reported in full. A wake
-- note waits for the sync, so it says how it went.
ALTER TABLE runs
  ADD COLUMN checkout_sync_id text,
  ADD COLUMN checkout_sync_repos text[] NOT NULL DEFAULT '{}',
  ADD COLUMN checkout_sync_at timestamptz,
  ADD COLUMN checkout_synced_at timestamptz,
  -- Why this conductor's checkout is read-only for the rest of its life:
  -- lux refused a safe sync mode (a lux from before them). NULL: writable,
  -- or not a conductor.
  ADD COLUMN checkout_read_only text;

-- Each publish a conductor asked for: requested by its tool, asked of lux
-- (asked), pushed (lux's git.push result), then published (the task
-- branch moved) or refused (nothing moved, and why). heads: per repository
-- published, {sha, base, changedPaths, lines}.
CREATE TABLE conductor_publishes (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id          text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id           text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  request_id       text NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'requested'
                   CHECK (status IN ('requested', 'asked', 'pushed', 'published', 'refused')),
  message          text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
  push_result      jsonb,
  heads            jsonb,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  settled_at       timestamptz
);
-- At most one in flight per conductor.
CREATE UNIQUE INDEX conductor_publishes_live_idx ON conductor_publishes (run_id)
  WHERE status IN ('requested', 'asked', 'pushed');
CREATE INDEX conductor_publishes_task_idx ON conductor_publishes (task_id, settled_at) WHERE status = 'published';

ALTER TABLE conductor_publishes ENABLE ROW LEVEL SECURITY;
ALTER TABLE conductor_publishes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conductor_publishes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON conductor_publishes TO dude_app;
-- The syncer finds publishes to carry on across organizations.
GRANT SELECT ON conductor_publishes TO dude_sweeper;

-- What woke the conductor about its edits: a publish's outcome, and how
-- its checkout's sync went when it did not simply fast-forward.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'checkout'));
