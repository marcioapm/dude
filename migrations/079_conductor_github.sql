-- 079_conductor_github.sql — the conductor and GitHub.

-- A conducted task's pull request merged or closed wakes its conductor
-- once, to close out (conductor_wakes kinds pr_merged, pr_closed).
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed'));
