-- 177_fake_checks_waiver_models.sql — the scripted agent's models for a
-- person's waiver later Runs carry: fake/checks-preexisting (every writer
-- reports one command failing the same way), fake/checks-new-failure (the
-- same command failing with a further failure) and
-- fake/checks-other-failure (another command failing) are test models a
-- tier may request. This check replaces the one standing after
-- 176_guide_sections, which is 172's unchanged, keeping every model it
-- allowed.

ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer', 'fake/machine', 'fake/test-fail', 'fake/test-fail-once', 'fake/test-blocked',
                   'fake/test-unverified', 'fake/test-silent', 'fake/test-fail-quiet', 'fake/done',
                   'fake/print-secrets', 'fake/commit-secrets',
                   'fake/checks-honest', 'fake/checks-unbacked', 'fake/checks-fixed',
                   'fake/checks-preexisting', 'fake/checks-new-failure', 'fake/checks-other-failure')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
