-- 133_feedback_pictures_kept.sql — a feedback item's picture is not swept.
--
-- A preview item's picture is an upload (attachments, 071) its anchor names
-- (feedback_items.anchor->>'attachmentId'); it stays unattached until a
-- message carries it, and the backend's sweeper deletes unattached uploads
-- a day old. The sweeper now keeps one any item names, draft or submitted,
-- and takes it on its next pass once no item does (the draft deleted, or
-- its anchor changed). This index serves that test; the column grant lets
-- the sweeper read anchors and nothing else of an item (no words, no
-- author).
--
-- No data changes. Safe to run again.

CREATE INDEX IF NOT EXISTS feedback_items_attachment_idx ON feedback_items ((anchor->>'attachmentId'))
  WHERE anchor->>'attachmentId' IS NOT NULL;

GRANT SELECT (anchor) ON feedback_items TO dude_sweeper;
