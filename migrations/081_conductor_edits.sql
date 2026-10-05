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

-- Each publish a conductor asked for, a durable, reconciled operation:
-- requested by its tool, asked of lux (asked), pushed (lux's git.push
-- result), then — once the comparisons pass — reserved (moving), under the
-- task's Chat lock, its conductor's Run and its delivery's row, with each
-- repository's intended move in moves: {repo: {repoId, slug, from, base,
-- head, status, error, changedPaths, lines, attemptedAt}}, status pending,
-- moved, refused or stalled, attemptedAt once its fast-forward was first
-- sent. While a publish is moving no writer starts, its conductor is not
-- replaced or ended, its decider does not change and its task does not
-- end. Each repository's result is kept as it happens, so a retry
-- reconciles what already moved with the forge before anything is judged
-- again. It settles published (heads: per repository published, {sha,
-- base, changedPaths, lines}), refused (nothing moved, and why), or
-- stalled: a sent move the forge would not confirm within 30 minutes,
-- none confirmed — nothing recorded as published, the fence released.
CREATE TABLE conductor_publishes (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id          text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id           text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  request_id       text NOT NULL UNIQUE,
  status           text NOT NULL DEFAULT 'requested'
                   CHECK (status IN ('requested', 'asked', 'pushed', 'moving', 'published', 'refused', 'stalled')),
  message          text NOT NULL DEFAULT '' CHECK (length(message) <= 2000),
  -- The delivery and attempt it was asked on, and their task branch.
  workflow_run_id  text,
  attempt          int,
  branch           text,
  push_result      jsonb,
  moves            jsonb NOT NULL DEFAULT '{}'::jsonb,
  heads            jsonb,
  error            text,
  -- When lux was asked to push, set with the status: an ask lux never
  -- answers is given up on.
  asked_at         timestamptz,
  -- How far the worker read the conductor's lux events for that push.
  lux_events_after bigint NOT NULL DEFAULT 0,
  -- The worker's schedule: not before this (a back-off after a transient
  -- failure), and how many transient failures in a row.
  next_attempt_at  timestamptz,
  failures         int NOT NULL DEFAULT 0,
  -- The worker carrying it on now: its claim, renewed while it works, and
  -- until when it holds. Every write of the worker's is guarded on it.
  claim_token      text,
  claimed_until    timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  settled_at       timestamptz
);
-- At most one in flight per conductor.
CREATE UNIQUE INDEX conductor_publishes_live_idx ON conductor_publishes (run_id)
  WHERE status IN ('requested', 'asked', 'pushed', 'moving');
CREATE INDEX conductor_publishes_task_idx ON conductor_publishes (task_id, settled_at) WHERE status = 'published';
-- The worker's queue: live publishes by when they are due.
CREATE INDEX conductor_publishes_due_idx ON conductor_publishes (next_attempt_at NULLS FIRST, created_at)
  WHERE status IN ('requested', 'asked', 'pushed', 'moving');
-- What fences a task's writers: its publish moving now.
CREATE INDEX conductor_publishes_moving_idx ON conductor_publishes (task_id) WHERE status = 'moving';

ALTER TABLE conductor_publishes ENABLE ROW LEVEL SECURITY;
ALTER TABLE conductor_publishes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON conductor_publishes
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON conductor_publishes TO dude_app;
-- The worker finds and claims publishes to carry on across organizations.
GRANT SELECT ON conductor_publishes TO dude_sweeper;
GRANT UPDATE (claim_token, claimed_until) ON conductor_publishes TO dude_sweeper;

-- What woke the conductor about its edits: a publish's outcome, one whose
-- move could not be confirmed, and how its checkout's sync went when it
-- did not simply fast-forward.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout'));

-- A workflow step that returned Wait is not run before this, not even for a
-- signal already in its inbox: the signal stays for the retry.
ALTER TABLE workflow_runs ADD COLUMN wait_until timestamptz;
