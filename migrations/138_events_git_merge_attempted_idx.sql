-- dude:no-transaction
-- A phase's own merge of its commit, after GitHub's answer to POST /merges
-- was lost (phases.Syncer.mergeAttempted): the Run's merge-attempt markers,
-- which events_run_cursor_idx finds only among every other event of the
-- Run, and events_type_idx only among every marker of the organization.
-- Built concurrently, so events are written throughout; a file of its own,
-- outside a transaction. IF NOT EXISTS makes a retry after a crash before it
-- was recorded a no-op.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_run_git_merge_attempted_idx ON events (run_id)
  WHERE event_type = 'git.merge_attempted';
