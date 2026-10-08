-- 096_session_names.sql — a session starts untitled and names itself.
--
-- title is NULL until someone names it: its agent (name_session) or a
-- member who can chat. titled_by says which named it last: once a person
-- has, the agent's tool refuses, so a person's title stays. It defaults
-- to 'person' because a title given with the row is one a person typed:
-- every title before this one was, and an untitled session is made with
-- an explicit NULL.

ALTER TABLE sessions ALTER COLUMN title DROP NOT NULL;
ALTER TABLE sessions DROP CONSTRAINT sessions_title_check;
ALTER TABLE sessions ADD COLUMN titled_by text DEFAULT 'person' CHECK (titled_by IN ('agent', 'person'));
ALTER TABLE sessions ADD CONSTRAINT sessions_title_check
  CHECK (title IS NULL OR length(btrim(title)) BETWEEN 1 AND 200);
ALTER TABLE sessions ADD CONSTRAINT sessions_titled_check CHECK ((title IS NULL) = (titled_by IS NULL));
