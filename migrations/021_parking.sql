-- A Run waiting on a person is parked: after a grace period, dude stops its
-- lux Run (state and conversation kept, no capacity held) and resumes it
-- when the person answers — minutes or days later. And a Run gone quiet is
-- nudged once, then parked for a person to look at. There is no wall-clock
-- limit on a Run.

-- Why dude paused a Run itself, so it knows when to resume it:
--   repository — to bring repositories a person approved: at once.
--   person     — parked while it waits on a person: once nothing is open.
--   idle       — gone quiet after a nudge: only when a person resumes it.
-- A person's own pause is not dude's (NULL), and is theirs to end.
ALTER TABLE runs ADD COLUMN dude_pause text CHECK (dude_pause IN ('repository', 'person', 'idle'));
UPDATE runs SET dude_pause = 'repository' WHERE paused_for_repository;
ALTER TABLE runs DROP COLUMN paused_for_repository;

ALTER TABLE runs
  -- The agent ended its turn with something open for a person (a question,
  -- a repository request). Parked once this is older than the grace period.
  ADD COLUMN waiting_since   timestamptz,
  -- When the agent last did something (said, thought, called a tool): how
  -- the idle check knows it has gone quiet.
  ADD COLUMN agent_active_at timestamptz,
  -- Tool calls started and not finished: an agent running a long command is
  -- not idle.
  ADD COLUMN open_tool_calls text[] NOT NULL DEFAULT '{}',
  -- When it was nudged for being quiet; cleared when it does something.
  ADD COLUMN idle_nudged_at  timestamptz;

-- The Run's agent no longer keeps the whole turn's reply: a question is
-- asked with a tool, not read out of the reply.
ALTER TABLE runs DROP COLUMN agent_turn_reply;

-- The phase syncer decides across organizations whether a Run waits on a
-- person, and how long the project lets it wait or stay quiet.
GRANT SELECT ON questions, projects TO dude_sweeper;
