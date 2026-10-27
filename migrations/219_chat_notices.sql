-- 219_chat_notices.sql — what dude posts in the chat thread a task came
-- from: its delivery started, a pull request opened, it was done, or it
-- failed or was stopped. An outbox: a row is recorded in the transaction
-- that moves the task (delivery.RecordStatusTx, the pull request's
-- recording), at most one per task and kind, and the chat-notices loop
-- posts it with no transaction open, retrying a transient failure with
-- backoff and giving up after 6 tries. What it says is fixed when it is
-- recorded: the task's key and title, the pull request's link or why it
-- failed, and the thread.
--
-- A new, empty table: no lock on anything that exists.
CREATE TABLE chat_notices (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id               text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind                  text NOT NULL CHECK (kind IN ('started', 'pr_opened', 'done', 'failed')),
  provider              text NOT NULL,
  external_workspace_id text NOT NULL,
  conversation_id       text NOT NULL,
  conversation_name     text NOT NULL DEFAULT '',
  -- '' posts at the conversation's top level (a direct message's).
  thread_id             text NOT NULL DEFAULT '',
  task_key              text NOT NULL,
  task_title            text NOT NULL,
  detail                text NOT NULL DEFAULT '' CHECK (length(detail) <= 2000),
  created_at            timestamptz NOT NULL DEFAULT now(),
  attempts              integer NOT NULL DEFAULT 0,
  next_attempt_at       timestamptz NOT NULL DEFAULT now(),
  posted_at             timestamptz,
  message_id            text,
  -- Set once dude stops trying: the last error, as the provider's adapter
  -- scrubbed it.
  gave_up_at            timestamptz,
  last_error            text CHECK (last_error IS NULL OR length(last_error) <= 2000),
  UNIQUE (task_id, kind)
);
-- The loop's pending rows, across tenants, due first.
CREATE INDEX chat_notices_pending_idx ON chat_notices (next_attempt_at, id)
  WHERE posted_at IS NULL AND gave_up_at IS NULL;
ALTER TABLE chat_notices ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_notices FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_notices
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_notices TO dude_app;
-- The loop finds what is due across tenants, then posts each in its org.
GRANT SELECT (id, organization_id, next_attempt_at, posted_at, gave_up_at) ON chat_notices TO dude_sweeper;
