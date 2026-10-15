-- 126_fake_done_model.sql — the scripted agent's fake/done (its implementer
-- finds the goal met and commits nothing, as one continuing finished work
-- does) is a test model a tier may request. This check replaces 123's whole,
-- keeping every model it allowed.

ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer', 'fake/machine', 'fake/test-fail', 'fake/test-fail-once', 'fake/test-blocked',
                   'fake/test-unverified', 'fake/test-silent', 'fake/test-fail-quiet', 'fake/done')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
