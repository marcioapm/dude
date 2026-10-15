-- 123_acceptance_verdicts.sql — the tester's verdict on each acceptance
-- criterion, bound to what it tested (delivery.RecordVerdictTx).
--
-- One row per (test Run, criterion), written once when the delivery reads
-- the finished test Run. `status` is dude's reading of the tester's
-- report: a `pass` whose evidence names no file this Run published is
-- `unverified`, a criterion the tester never answered is `blocked`, and
-- `problem` says why dude changed what the tester said. `heads` is the
-- commit per repository the Run checked out: a verdict covers those and
-- no later ones. `decision` is a person's: `waived` (a blocked check) or
-- `accepted` (a failed one), with who and why.
--
-- A new table only: nothing existing is rewritten or locked.

CREATE TABLE acceptance_verdicts (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id         text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id          text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  -- 1-based, into the task's acceptance criteria (or the tester's derived
  -- checks when the task has none).
  criterion       integer NOT NULL CHECK (criterion > 0),
  text            text NOT NULL,
  status          text NOT NULL CHECK (status IN ('pass', 'fail', 'blocked', 'unverified', 'not-applicable')),
  evidence        text[] NOT NULL DEFAULT '{}',
  observed        text NOT NULL DEFAULT '',
  problem         text NOT NULL DEFAULT '',
  heads           jsonb NOT NULL DEFAULT '{}',
  decision        text CHECK (decision IN ('waived', 'accepted')),
  decided_by      text REFERENCES people(id) ON DELETE SET NULL,
  decision_note   text,
  decided_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, criterion),
  CHECK ((decision IS NULL) = (decided_at IS NULL))
);

CREATE INDEX acceptance_verdicts_task_idx ON acceptance_verdicts (task_id);

ALTER TABLE acceptance_verdicts ENABLE ROW LEVEL SECURITY;
ALTER TABLE acceptance_verdicts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON acceptance_verdicts
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON acceptance_verdicts TO dude_app;
