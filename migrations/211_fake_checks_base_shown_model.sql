-- 211_fake_checks_base_shown_model.sql — the scripted agent's
-- fake/checks-base-shown (every writer runs its failing command at the
-- commit it started from, then on its change, and reports the same failure
-- both times) is a test model a tier may request. This check replaces 177's
-- whole, keeping every model it allowed.

ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer', 'fake/machine', 'fake/test-fail', 'fake/test-fail-once', 'fake/test-blocked',
                   'fake/test-unverified', 'fake/test-silent', 'fake/test-fail-quiet', 'fake/done',
                   'fake/print-secrets', 'fake/commit-secrets',
                   'fake/checks-honest', 'fake/checks-unbacked', 'fake/checks-fixed',
                   'fake/checks-preexisting', 'fake/checks-new-failure', 'fake/checks-other-failure',
                   'fake/checks-base-shown')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
