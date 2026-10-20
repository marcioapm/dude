-- 159_session_attachments.sql — an image may be a brainstorm session's.
--
-- A member's message in a session's Chat may carry images, as a steer does
-- (071). The upload belongs to the session it was made in, and to no task:
-- task_id goes nullable and session_id joins it, exactly one of the two set.
-- A session's upload is only ever attached to a directive of its agent;
-- for_prompt (a task's text) stays a task's alone.
--
-- Its objects are queued for deletion as a task's are: the session's
-- cascade, the sweep of unsent uploads and a person's removal all delete the
-- row, and 071's trigger queues both keys.

ALTER TABLE attachments ALTER COLUMN task_id DROP NOT NULL;
ALTER TABLE attachments ADD COLUMN session_id text REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE attachments ADD CONSTRAINT attachments_one_owner CHECK ((task_id IS NULL) <> (session_id IS NULL));
ALTER TABLE attachments ADD CONSTRAINT attachments_session_not_prompt CHECK (session_id IS NULL OR NOT for_prompt);
CREATE INDEX attachments_session_idx ON attachments (session_id) WHERE session_id IS NOT NULL;
