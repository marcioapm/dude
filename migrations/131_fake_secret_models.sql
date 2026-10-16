-- 131_fake_secret_models.sql — the scripted agent's fake/print-secrets
-- (prints the env secrets its Run was given) and fake/commit-secrets (its
-- implementer commits them) are test models a tier may request, for the
-- project secrets suites. This check replaces 126's whole, keeping every
-- model it allowed.

ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer', 'fake/machine', 'fake/test-fail', 'fake/test-fail-once', 'fake/test-blocked',
                   'fake/test-unverified', 'fake/test-silent', 'fake/test-fail-quiet', 'fake/done',
                   'fake/print-secrets', 'fake/commit-secrets')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
