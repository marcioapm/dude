-- Whether a pull request's CI has started on its head is not something
-- GitHub says: a head with no statuses or check runs yet reads the same as
-- a repository with no CI. So dude keeps what it needs to tell them apart:
-- whether this pull request has ever had CI, and when its current head was
-- first seen. Unknown on a pull request that has had CI is CI yet to start
-- while its head is new, and no CI on it (a commit CI skips) after that.
ALTER TABLE pull_requests
  ADD COLUMN had_ci boolean NOT NULL DEFAULT false,
  ADD COLUMN head_seen_at timestamptz;
UPDATE pull_requests SET had_ci = checks <> 'unknown', head_seen_at = updated_at;
