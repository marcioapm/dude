-- 271_answer_reconciliations.sql — a fixer's held answers in flight when
-- its delivery ended (#485).
--
-- A delivery that ends (merged, closed, stopped) withholds the fixers'
-- held answers nobody claimed (delivery.settleHeldOnEndTx) and blocks new
-- claims (pull_request.answers_ended, migration 272). An answer a poster
-- had claimed may already be on GitHub, or be about to be: it is never
-- withheld there. Its poster records what happened (posted, failed); one
-- that is gone leaves it to the pr-reconciler's pass
-- (delivery.ReconcileEndedAnswers), which, once due_at passes (the claim's
-- lease), reads GitHub for dude's own marked comment, or the thread's
-- state, and settles it: posted, withheld (conclusively not there) or
-- uncertain (it may have been posted: someone is told to check). One row
-- per answer (its EvAnswerHeld id); outcome is set once, with the notice.
--   ended       why the delivery ended, as the withheld answers say.
--   label       what the answer answers (a feedback id, or "resolving …").
--   due_at      not read before then: the claim's lease, or a retry.
--   settled_at  when its outcome was recorded and told; NULL while open.
CREATE TABLE answer_reconciliations (
  answer_id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  ended text NOT NULL CHECK (length(ended) <= 300),
  label text NOT NULL CHECK (length(label) <= 300),
  created_at timestamptz NOT NULL DEFAULT now(),
  due_at timestamptz NOT NULL,
  settled_at timestamptz,
  outcome text CHECK (outcome IN ('posted', 'failed', 'withheld', 'uncertain')),
  CHECK ((settled_at IS NULL) = (outcome IS NULL))
);
-- The pass's candidates: open, by when they are due. Settled rows leave it.
CREATE INDEX answer_reconciliations_due_idx ON answer_reconciliations (due_at) WHERE settled_at IS NULL;
CREATE INDEX answer_reconciliations_task_idx ON answer_reconciliations (task_id);
ALTER TABLE answer_reconciliations ENABLE ROW LEVEL SECURITY;
ALTER TABLE answer_reconciliations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON answer_reconciliations
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON answer_reconciliations TO dude_app;
-- The pass's cross-tenant probe for rows due; each is settled in its
-- organization's own transaction.
GRANT SELECT ON answer_reconciliations TO dude_sweeper;
