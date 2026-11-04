-- 242_question_addressee.sql — conductor-first escalation, PR1: who a
-- question is for. Every existing row is a person's, which is what the
-- default says; nothing writes 'conductor' yet (delivery.ToPersonTx is the
-- one writer of a person's question on a task).
--   addressee        'person', or 'conductor': a writer's question its
--                    conductor answers first (ask_conductor, a later PR).
--   answered_by_run  the conductor Run that answered it, when one did.
--   relays           on a conductor's question for the person, the writer's
--                    question it relays: the person's answer answers both.
--
-- Catalog changes only, as in 103 and 106: a constant default rewrites
-- nothing, and the check and both foreign keys are NOT VALID, so they bind
-- every write from now on without scanning questions under the ACCESS
-- EXCLUSIVE lock (nor holding runs' SHARE ROW EXCLUSIVE through one). Every
-- existing row holds 'person' and NULL, which satisfies them: there is
-- nothing for a scan to find. The deadline sweep's index is built
-- concurrently by 243, outside this lock.
ALTER TABLE questions
  ADD COLUMN addressee text NOT NULL DEFAULT 'person',
  ADD COLUMN answered_by_run text,
  ADD COLUMN relays text;
ALTER TABLE questions ADD CONSTRAINT questions_addressee_check
  CHECK (addressee IN ('person', 'conductor')) NOT VALID;
ALTER TABLE questions ADD CONSTRAINT questions_answered_by_run_fkey
  FOREIGN KEY (answered_by_run) REFERENCES runs(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE questions ADD CONSTRAINT questions_relays_fkey
  FOREIGN KEY (relays) REFERENCES questions(id) ON DELETE SET NULL NOT VALID;
