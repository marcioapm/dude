-- 242_question_addressee.sql — conductor-first escalation, PR1: who a
-- question is for. Every existing row is a person's, which is what the
-- default says; nothing writes 'conductor' yet (delivery.ToPersonTx is the
-- one writer of a person's question on a task).
--   addressee        'person', or 'conductor': a writer's question its
--                    conductor answers first (ask_conductor, a later PR).
--   answered_by_run  the conductor Run that answered it, when one did.
--   relays           on a conductor's question for the person, the writer's
--                    question it relays: the person's answer answers both.
-- A constant default is a catalog change only: no table rewrite.
ALTER TABLE questions
  ADD COLUMN addressee text NOT NULL DEFAULT 'person' CONSTRAINT questions_addressee_check CHECK (addressee IN ('person', 'conductor')),
  ADD COLUMN answered_by_run text REFERENCES runs(id) ON DELETE SET NULL,
  ADD COLUMN relays text REFERENCES questions(id) ON DELETE SET NULL;

-- The deadline sweep's: a task's open questions with its conductor.
CREATE INDEX questions_conductor_open_idx ON questions (task_id) WHERE status = 'open' AND addressee = 'conductor';
