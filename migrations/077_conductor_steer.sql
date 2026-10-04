-- 077_conductor_steer.sql — the conductor steers the Runs it conducts.

-- The conductor Run that wrote a directive; NULL for a person's (or dude's
-- own: a wake note, an answer). Its read or failure wakes that conductor's
-- task (conductor_wakes kinds steer_read, steer_failed).
ALTER TABLE directives ADD COLUMN conductor_run_id text REFERENCES runs(id) ON DELETE SET NULL;

ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed'));

-- The scripted agent's fake/command (an implementer in a long command, to
-- steer mid-turn) is a test model a tier may request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/command')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
