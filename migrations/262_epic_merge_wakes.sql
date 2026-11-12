-- 262_epic_merge_wakes.sql — a merge on an epic wakes its peers' conductors
-- (delivery.EpicMerges).
--
-- pull_requests.merge_commit_sha: the commit GitHub's merge made, as the
-- pull request sync reads it; NULL when it was never read.
--
-- pull_requests.epic_peers_told_at: when the worker told the epic peers of
-- a merged pull request's task. The worker takes every merged pull request
-- with none (level-triggered: the stored state, not the sync's event), and
-- sets it in the transaction that records the wakes. A pull request merged
-- before this migration is marked told: its peers are not woken for it now.
--
-- conductor_wakes: kind epic_merge, its line bounded at 300 like the rest.
-- NOT VALID: no scan of the table under ACCESS EXCLUSIVE; every row there
-- holds a kind the constraint it replaces allowed, all of which this allows.
ALTER TABLE pull_requests
  ADD COLUMN merge_commit_sha   text CHECK (merge_commit_sha ~ '^[0-9a-f]{40,64}$'),
  ADD COLUMN epic_peers_told_at timestamptz;

UPDATE pull_requests SET epic_peers_told_at = COALESCE(merged_at, now()) WHERE state = 'merged';

-- The worker's candidates: merged, not yet told.
CREATE INDEX pull_requests_epic_untold_idx ON pull_requests (merged_at)
  WHERE state = 'merged' AND epic_peers_told_at IS NULL;

ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback', 'wait',
                  'recovered', 'round', 'checks', 'epic_merge')) NOT VALID;
