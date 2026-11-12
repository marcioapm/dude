-- 265_epic_merge_wakes.sql — a merge on an epic wakes its peers' conductors
-- (delivery.EpicMerges).
--
-- merge_commit_sha: GitHub's merge commit, as the pull request sync reads it.
-- epic_peers_told_at: when the worker told the peers; it takes every merged
-- pull request without one. Those merged before this migration count as told.
-- epic_peers_retry_at: a merge the worker could not settle waits until then.
--
-- conductor_wakes gains kind epic_merge (its line bounded at 300 by 264's
-- line check, as the rest). NOT VALID: no scan under ACCESS EXCLUSIVE;
-- every kind 264 allows, workflow included, is still allowed.
ALTER TABLE pull_requests
  ADD COLUMN merge_commit_sha    text CHECK (merge_commit_sha ~ '^[0-9a-f]{40,64}$'),
  ADD COLUMN epic_peers_told_at  timestamptz,
  ADD COLUMN epic_peers_retry_at timestamptz;

UPDATE pull_requests SET epic_peers_told_at = COALESCE(merged_at, now()) WHERE state = 'merged';

-- The worker's candidates: merged, not yet told.
CREATE INDEX pull_requests_epic_untold_idx ON pull_requests (merged_at)
  WHERE state = 'merged' AND epic_peers_told_at IS NULL;

ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'workflow', 'epic_merge')) NOT VALID;
