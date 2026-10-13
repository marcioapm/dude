-- 111_agent_machine_moves.sql — an agent moves itself to a larger machine
-- (docs/design/agent-machine-request.md).
--
-- An agent's request_machine, and a person's Reset, are each a move: the
-- Run is parked (dude_pause 'machine') and resumed at once on the size its
-- task (or session) now names, in that size's pool. The size a raise asked
-- for is kept per role on the task (tasks.agent_machines), so every later
-- Run of the role in the task, Start over's attempts included, runs on it;
-- a session keeps its own (sessions.agent_machine). Reset drops it.
--
-- New columns are catalog-only: a constant default (or none) rewrites no
-- row, so tasks' and sessions' ACCESS EXCLUSIVE locks are held for the
-- catalog update alone, as 109's.

CREATE TABLE machine_moves (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id         text REFERENCES tasks(id) ON DELETE CASCADE,
  session_id      text REFERENCES sessions(id) ON DELETE CASCADE,
  -- The Run moved.
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  -- Settings role, conductor and brainstorm included.
  role            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('raise', 'reset')),
  -- runs.machine before the move.
  from_size       jsonb NOT NULL,
  -- The size moved to. A size's removal moves an unsettled move to its
  -- replacement, as it moves the task and session layers; a settled move's
  -- target is cleared, its event payloads keeping the size as it was.
  to_size_id      text REFERENCES machine_sizes(id) ON DELETE SET NULL,
  -- 'none': a Reset with nothing running, which only clears the layer.
  apply           text NOT NULL CHECK (apply IN ('resize', 'none')),
  -- {from, to}: lux pool ids, when the move changed pools.
  pool            jsonb,
  -- The agent's reason; a Reset's optional note.
  reason          text CHECK (length(reason) <= 2000),
  by_person       text REFERENCES people(id) ON DELETE SET NULL,
  -- lux's code, when the real resume refused the move.
  refused         text,
  refused_message text,
  -- The Run resumed: moved, or refused and left where it was.
  settled_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK ((task_id IS NULL) <> (session_id IS NULL))
);
-- One move of a Run at a time: a move is settled when the Run resumes.
CREATE UNIQUE INDEX machine_moves_one_at_a_time ON machine_moves (run_id) WHERE settled_at IS NULL;
CREATE INDEX machine_moves_task_idx ON machine_moves (task_id, created_at) WHERE task_id IS NOT NULL;
CREATE INDEX machine_moves_session_idx ON machine_moves (session_id, created_at) WHERE session_id IS NOT NULL;

ALTER TABLE machine_moves ENABLE ROW LEVEL SECURITY;
ALTER TABLE machine_moves FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON machine_moves
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON machine_moves TO dude_app;
-- The phase syncer settles moves at the resume it makes across organisations.
GRANT SELECT ON machine_moves TO dude_sweeper;

-- Per settings role (conductor included): {sizeId, moveId, askedBy, reason, at}.
ALTER TABLE tasks ADD COLUMN agent_machines jsonb NOT NULL DEFAULT '{}';
-- A session's own raise: the same object, with no role key.
ALTER TABLE sessions ADD COLUMN agent_machine jsonb;

-- machine: parked so the resume moves it to another size; resumed at once.
ALTER TABLE runs DROP CONSTRAINT runs_dude_pause_check;
ALTER TABLE runs ADD CONSTRAINT runs_dude_pause_check
  CHECK (dude_pause IN ('repository', 'person', 'idle', 'unused', 'conductor', 'session', 'machine'));
ALTER TABLE run_resumes DROP CONSTRAINT run_resumes_cause_check;
ALTER TABLE run_resumes ADD CONSTRAINT run_resumes_cause_check
  CHECK (cause IN ('answer', 'repository', 'person', 'idle', 'conductor', 'session', 'machine'));

-- The scripted agent's fake/machine (its implementer asks for a larger
-- machine and ends its turn there) is a test model a tier may request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/machine')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
