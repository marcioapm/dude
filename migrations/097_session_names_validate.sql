-- 097_session_names_validate.sql — validate 096's checks on the rows before it.
--
-- VALIDATE CONSTRAINT scans the table under SHARE UPDATE EXCLUSIVE, which
-- blocks neither reads nor writes; the rows written since 096 already
-- obeyed the checks. A row that breaks one (written by hand, or by a
-- process that bypassed them) stops the migration, naming the constraint:
-- fix the row, then migrate again.

ALTER TABLE sessions VALIDATE CONSTRAINT sessions_titled_by_check;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_title_check;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_titled_check;
