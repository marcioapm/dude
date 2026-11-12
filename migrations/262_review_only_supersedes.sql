-- 262_review_only_supersedes.sql — a third way to take over work, and a
-- task that replaces another on the same pull request or branch.
--
-- task_sources.mode 'review_only': dude reviews a person's pull request,
-- changes no code and posts a GitHub review. Its repository is attached
-- read-only, and no writer Run is ever made for it (the orchestrator's
-- createPhaseRunTx guard). 125 added the CHECK unnamed; Postgres named it
-- task_sources_mode_check, which this drops by looking the name up so a
-- database that named it otherwise still migrates.
--
-- task_relation_kind 'supersedes': from a new task to the live one it
-- stopped to take over the same pull request or branch, written once, by
-- the supersede itself. Directed, like `blocks`; it gates nothing.
-- ALTER TYPE … ADD VALUE: nothing here uses the value in this transaction.

DO $$
DECLARE
  c text;
BEGIN
  FOR c IN
    SELECT con.conname FROM pg_constraint con
     WHERE con.conrelid = 'task_sources'::regclass AND con.contype = 'c'
       AND pg_get_constraintdef(con.oid) LIKE '%mode%'
  LOOP
    EXECUTE format('ALTER TABLE task_sources DROP CONSTRAINT %I', c);
  END LOOP;
END
$$;
ALTER TABLE task_sources ADD CONSTRAINT task_sources_mode_check
  CHECK (mode IN ('continue', 'review', 'review_only'));

ALTER TYPE task_relation_kind ADD VALUE IF NOT EXISTS 'supersedes';
