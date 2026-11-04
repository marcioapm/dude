-- 240_strip_pull_request_fixes.sql — every fix reaches an open pull request
-- directly now: the retired `pullRequestFixes` delivery key ("candidate" or
-- "direct") is dropped from the policies organisations and projects stored,
-- so nothing reads or shows it again. Every other key is left as it is; a
-- task's frozen policy is the orchestrator's candidate sweep's to clean.

UPDATE organizations SET delivery_policy = delivery_policy - 'pullRequestFixes'
  WHERE delivery_policy ? 'pullRequestFixes';
UPDATE projects SET delivery_policy = delivery_policy - 'pullRequestFixes'
  WHERE delivery_policy ? 'pullRequestFixes';
