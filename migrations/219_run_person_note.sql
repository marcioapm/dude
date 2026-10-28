-- 219_run_person_note.sql — a person's words, for the Run that acts on them.
--
-- person_note: what a person said with the decision this Run was started
-- for (an escalation's retry, accept or amend, a pick-up's Try again or
-- Start over), verbatim and marked as theirs ("The task's owner, Ola, said:
-- …"). Its prompt carries it ahead of the rest, as a resumed Run's next
-- message does. NULL for a Run no person's words started.
ALTER TABLE runs ADD COLUMN person_note text;

-- Decided escalation wakes carry a person's complete words, not a clipped
-- summary. Keep the bounds on machine-generated facts of the other kinds.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_line_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_line_check
  CHECK (kind = 'escalation' OR length(line) <= CASE kind WHEN 'stalled' THEN 2000 ELSE 300 END) NOT VALID;
