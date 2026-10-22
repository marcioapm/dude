-- 175_review_questions.sql
-- A reviewer's question: a fact it could not establish from the code, its
-- configuration or its history, which would change a severity or a verdict.
-- Not `questions` (an agent's ask_person, cancelled when its Run ends, and
-- answered into a live Run) and not review_findings (no fix, no severity):
-- it is asked by a review Run that has already ended, and is shown to the
-- next reviewer of its category.
CREATE TABLE review_questions (
  id                 text PRIMARY KEY,
  organization_id    text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id            text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  -- The review Run that asked, and the question's place in its reply: a
  -- retried report inserts nothing twice and overwrites no answer.
  run_id             text REFERENCES runs(id) ON DELETE SET NULL,
  n                  integer NOT NULL CHECK (n > 0),
  -- The asking Run's flavour: the next reviewer of it is shown the question.
  category           text NOT NULL,
  -- `about: F<n>`, the earlier finding the asking Run was shown at n.
  finding_id         text REFERENCES review_findings(id) ON DELETE SET NULL,
  question           text NOT NULL CHECK (question <> ''),
  why_it_matters     text NOT NULL DEFAULT '',
  evidence_checked   text NOT NULL DEFAULT '',
  needed_from        text NOT NULL DEFAULT '',
  status             text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'withdrawn')),
  answer             text,
  answered_by_person text REFERENCES people(id) ON DELETE SET NULL,
  -- The conductor's Run, when it answered.
  answered_by_run    text REFERENCES runs(id) ON DELETE SET NULL,
  answered_at        timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, n),
  CHECK ((status = 'answered') = (answer IS NOT NULL))
);

-- A task's questions of every status in the order the API lists them.
CREATE INDEX review_questions_task_idx ON review_questions (task_id, created_at, run_id, n);

ALTER TABLE review_questions ENABLE ROW LEVEL SECURITY;
ALTER TABLE review_questions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON review_questions
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON review_questions TO dude_app;
