-- 096_session_names.sql — a session starts untitled and names itself.
--
-- title is NULL until someone names it: its agent (name_session) or a
-- member who can chat. titled_by says which named it last: once a person
-- has, the agent's tool refuses, so a person's title stays. It defaults
-- to 'person' because a title given with the row is one a person typed:
-- every title before this one was, and an untitled session is made with
-- an explicit NULL.
--
-- Every statement here changes the catalog only: a constant default adds
-- the column without a rewrite, and the checks are NOT VALID, so they bind
-- every write from now on without scanning the rows under this
-- transaction's ACCESS EXCLUSIVE lock. 097 validates them under SHARE
-- UPDATE EXCLUSIVE, which lets reads and writes carry on.

ALTER TABLE sessions ALTER COLUMN title DROP NOT NULL;
ALTER TABLE sessions DROP CONSTRAINT sessions_title_check;
ALTER TABLE sessions ADD COLUMN titled_by text DEFAULT 'person';
ALTER TABLE sessions ADD CONSTRAINT sessions_titled_by_check
  CHECK (titled_by IN ('agent', 'person')) NOT VALID;
ALTER TABLE sessions ADD CONSTRAINT sessions_title_check
  CHECK (title IS NULL OR length(btrim(title)) BETWEEN 1 AND 200) NOT VALID;
ALTER TABLE sessions ADD CONSTRAINT sessions_titled_check
  CHECK ((title IS NULL) = (titled_by IS NULL)) NOT VALID;
