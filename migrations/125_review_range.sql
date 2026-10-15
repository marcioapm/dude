-- 124_review_range.sql — a review Run limited to a range of commits
-- (delivery.ReviewRange): what the simplifier changed, or a fix for an open
-- pull request's feedback. `of` says which (`simplify`, `pull_request_fix`)
-- and `base` is the commit per repository the range starts after; the
-- reviewer is told `<base>..HEAD`. NULL reviews the whole branch, as every
-- review before this did.
--
-- A nullable column with no default: no row is rewritten, and the ALTER
-- holds its ACCESS EXCLUSIVE lock on runs only for the catalog change.

ALTER TABLE runs ADD COLUMN review_range jsonb;
