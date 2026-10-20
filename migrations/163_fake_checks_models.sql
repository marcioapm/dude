-- 163_fake_checks_models.sql — the scripted agent's checks-gate models:
-- fake/checks-honest (runs its check, sees it pass, reports it),
-- fake/checks-unbacked (sees it fail and reports it passing) and
-- fake/checks-fixed (does that once, then, sent back, runs it passing)
-- are test models a tier may request. This check replaces 131's whole,
-- keeping every model it allowed.

ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup', 'fake/subagent', 'fake/subagent-ask',
                   'fake/peer', 'fake/machine', 'fake/test-fail', 'fake/test-fail-once', 'fake/test-blocked',
                   'fake/test-unverified', 'fake/test-silent', 'fake/test-fail-quiet', 'fake/done',
                   'fake/print-secrets', 'fake/commit-secrets',
                   'fake/checks-honest', 'fake/checks-unbacked', 'fake/checks-fixed')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));
