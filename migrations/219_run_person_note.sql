-- 219_run_person_note.sql — a person's words, for the Run that acts on them.
--
-- person_note: what a person said with the decision this Run was started
-- for (an escalation's retry, accept or amend, a pick-up's Try again or
-- Start over), verbatim and marked as theirs ("The task's owner, Ola, said:
-- …"). Its prompt carries it ahead of the rest, as a resumed Run's next
-- message does. NULL for a Run no person's words started.
ALTER TABLE runs ADD COLUMN person_note text CHECK (length(person_note) <= 20000);
