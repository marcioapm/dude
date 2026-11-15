-- 267_author_questions.sql
-- Review only (PR 6b): the conductor asks the pull request's author on
-- GitHub (ask_author), and the open question holds dude's verdict. It is a
-- review_questions row like a reviewer's, addressed to 'author': asked by
-- the conductor's Run (run_id, the next n, category 'conductor'), with
-- the GitHub comment that asked it (github_comment_id, a feedback id such
-- as issue-comment-…; NULL while GitHub's answer to the post is unknown)
-- and the login it was asked of (asked_of). github_refused marks a question
-- GitHub did not take, withdrawn at once: it never held the verdict, so it
-- is not one of the settled questions an approval's claim names. (A
-- constant default: no table rewrite.)
--
-- The handover sweep and withdraw_held_review_questions read addressee
-- 'conductor' only (248): an author's question is never handed to a person
-- by them; it waits on GitHub. Every existing row keeps its addressee.
-- The widened CHECK is added NOT VALID (as 242): every existing row is
-- 'person' or 'conductor', so no scan of review_questions under ACCESS
-- EXCLUSIVE is needed; new and updated rows are checked all the same.
ALTER TABLE review_questions DROP CONSTRAINT review_questions_addressee_check;
ALTER TABLE review_questions ADD CONSTRAINT review_questions_addressee_check
  CHECK (addressee IN ('person', 'conductor', 'author')) NOT VALID;

ALTER TABLE review_questions
  ADD COLUMN github_comment_id text,
  ADD COLUMN github_url text,
  ADD COLUMN asked_of text,
  ADD COLUMN github_refused boolean NOT NULL DEFAULT false;

-- The syncer reads a task's open author questions on each pass.
CREATE INDEX review_questions_author_open_idx ON review_questions (task_id, created_at)
  WHERE status = 'open' AND addressee = 'author';
