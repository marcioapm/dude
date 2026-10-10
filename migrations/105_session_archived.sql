-- 105_session_archived.sql — a member archives a session for themselves.
--
-- Archiving is one person's: it takes the session out of their list and
-- sidebar, and nobody else's. It changes nothing else — membership, the
-- agent, files, proposals and invitations stay as they were — so it lives
-- on the person's own row. NULL is not archived. Leaving the session
-- (removal, declining, a handover with leave) deletes the row and the mark
-- with it; someone invited again starts unarchived.
--
-- Catalog change only: a nullable column with no default adds no rewrite.

ALTER TABLE session_people ADD COLUMN archived_at timestamptz;
