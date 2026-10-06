-- 083_escalation_questions.sql — the conductor's question about an
-- escalation, and the person's answer to it, decide the escalation.

-- escalation: the escalation a conductor's question asks about,
-- '<workflow run>:<number>' (State.Escalations when it was raised). NULL
-- for every other question. actions: what each of its options stands for,
-- by position (an escalation's actions: retry, accept, stop…): the owner
-- picking an option decides the escalation with that action.
-- answered_by_person: the person who answered, which a conductor deciding
-- the escalation on a free answer checks is the task's owner.
ALTER TABLE questions
  ADD COLUMN escalation text,
  ADD COLUMN actions jsonb,
  ADD COLUMN answered_by_person text REFERENCES people(id) ON DELETE SET NULL;

CREATE INDEX questions_escalation_idx ON questions (task_id, escalation) WHERE escalation IS NOT NULL;
