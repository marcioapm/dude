-- 268_answer_reconciliations.sql — a pull request fixer's held answers
-- that a poster had claimed when their delivery ended (#485).
--
-- A delivery that ends (merged, closed, stopped) withholds its fixers'
-- held answers nobody claimed (delivery.settleHeldOnEndTx). One a poster
-- claimed may be on GitHub, or about to be, so it is never withheld there:
-- it gets a row here instead, which also stops any new claim on it. Its
-- poster records what happened and sets outcome; a poster that is gone
-- leaves it to the pr-reconciler's pass (delivery.ReconcileEndedAnswers),
-- which, once due_at passes, reads GitHub and sets outcome: posted,
-- withheld, or uncertain. The outcome is said once, when it is set.
--   answer_id   the held answer (its pull_request.answer_held event id).
--   ended       why the delivery ended, as the end's wake said it.
--   claimed_at  the answer's latest claim: retries give up an hour past it.
--   due_at      not read from GitHub before then.
CREATE TABLE answer_reconciliations (
  answer_id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  ended text NOT NULL CHECK (length(ended) <= 300),
  claimed_at timestamptz NOT NULL,
  due_at timestamptz NOT NULL,
  outcome text CHECK (outcome IN ('posted', 'failed', 'withheld', 'uncertain'))
);
-- The pass's selection: open rows, by when they are due.
CREATE INDEX answer_reconciliations_due_idx ON answer_reconciliations (due_at) WHERE outcome IS NULL;
ALTER TABLE answer_reconciliations ENABLE ROW LEVEL SECURITY;
ALTER TABLE answer_reconciliations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON answer_reconciliations
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
-- A row is its answer's outcome: never deleted by the app.
REVOKE DELETE ON answer_reconciliations FROM dude_app;
-- The pass's cross-tenant probe for rows due; each is settled in its
-- organization's own transaction.
GRANT SELECT ON answer_reconciliations TO dude_sweeper;
