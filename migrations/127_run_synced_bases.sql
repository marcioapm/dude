-- 127_run_synced_bases.sql — the commit lux reported fetching for each base
-- a Run synced (its git.sync `to`), by lux's repository name, then ref:
-- {"target": {"main": "<sha>"}}. Lux fetches on the host, so this is the base
-- as dude can trust it; the checkout's refs/remotes/lux/<ref> is the agent's
-- to rewrite. A publish leaves out only what this commit, checked against
-- GitHub's base branch, brought in.
--
-- A column with a constant default: no table rewrite.

ALTER TABLE runs ADD COLUMN synced_bases jsonb NOT NULL DEFAULT '{}';
