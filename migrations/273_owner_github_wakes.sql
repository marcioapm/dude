-- 273_owner_github_wakes.sql — a conducted delivery whose pull request
-- waits on its owner connecting GitHub tells its conductor so
-- (delivery.WakeOwnerGitHub), for it to answer "what's next?" truthfully.
--
-- conductor_wakes gains kind owner_github, its line bounded at 300 by 271's
-- line check, as the rest. NOT VALID: no scan under ACCESS EXCLUSIVE; every
-- kind 265 allows is still allowed.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'workflow', 'epic_merge', 'owner_github')) NOT VALID;
