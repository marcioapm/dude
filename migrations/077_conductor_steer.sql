-- 077_conductor_steer.sql — the conductor steers the Runs it conducts.

-- The conductor Run that wrote a directive; NULL for a person's (or dude's
-- own: a wake note, an answer). Its read or failure wakes that conductor's
-- task (conductor_wakes kinds steer_read, steer_failed).
ALTER TABLE directives ADD COLUMN conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL;

ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed'));
