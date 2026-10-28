-- 219_chat_notices.sql — chat-origin task progress outbox, recorded in the
-- status/PR transaction once per task and kind. Snapshots the task's key,
-- title, allow-listed failure reason and thread. Posts outside transactions,
-- retrying transient failures for 6 hours. At least once: a crash after
-- posting can repeat the post when the claim's lease expires.
--
-- Catalog-only (no scan, no backfill); the foreign keys take SHARE ROW
-- EXCLUSIVE on tasks and organizations, briefly, until this commits.
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
