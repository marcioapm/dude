-- 219_chat_notices.sql — chat progress outbox, recorded in the status/PR
-- transaction once per subject and kind. A row is about one task (started,
-- PR opened, done, failed or stopped) or one epic (started, first PR
-- opened, finished with its counts), never both. Snapshots the subject's
-- key and title, an allow-listed detail and the thread. Posts outside
-- transactions, retrying transient failures for 6 hours. At least once: a
-- crash after posting can repeat the post when the claim's lease expires.
--
-- Catalog-only (no scan, no backfill); the foreign keys take SHARE ROW
-- EXCLUSIVE on tasks, epics and organizations, briefly, until this commits.
CREATE TABLE chat_notices (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id               text REFERENCES tasks(id) ON DELETE CASCADE,
  epic_id               text REFERENCES epics(id) ON DELETE CASCADE,
  kind                  text NOT NULL CHECK (kind IN ('started', 'pr_opened', 'done', 'failed',
                                                      'epic_started', 'epic_pr_opened', 'epic_finished')),
  provider              text NOT NULL,
  external_workspace_id text NOT NULL,
  conversation_id       text NOT NULL,
  conversation_name     text NOT NULL DEFAULT '',
  -- '' posts at the conversation's top level (a direct message's).
  thread_id             text NOT NULL DEFAULT '',
  -- A task's key (BIL-12), or an epic's project key (BIL), as the UI labels it.
  subject_key           text NOT NULL,
  subject_title         text NOT NULL,
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
  -- Exactly one subject, and an epic's kinds only on an epic.
  CONSTRAINT chat_notices_one_subject CHECK (num_nonnulls(task_id, epic_id) = 1
    AND (epic_id IS NOT NULL) = (kind LIKE 'epic\_%')),
  UNIQUE (task_id, kind),
  UNIQUE (epic_id, kind)
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
