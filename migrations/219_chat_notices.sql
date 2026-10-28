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
  -- An epic notice's task whose move or leaving recorded it: where a
  -- given-up notice is said.
  trigger_task_id       text REFERENCES tasks(id) ON DELETE SET NULL,
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
    AND (epic_id IS NOT NULL) = (kind LIKE 'epic\_%')
    AND (trigger_task_id IS NULL OR epic_id IS NOT NULL)),
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

-- Records an epic's notice of p_kind once per epic ever, about the task
-- p_trigger_task_id moved or took out of it, when the epic's origin resolves
-- to a conversation (its own, or the session's it was filed from).
-- epic_finished is recorded only while the epic has a non-archived task and
-- every one is done, failed or aborted, with the counts. Level-triggered:
-- called on every move to an end and whenever a task leaves the epic's
-- current set, so the last unfinished one leaving finishes it too.
--
-- The caller holds the task's row. The advisory lock comes next, so two of
-- the epic's tasks ending at once count each other's commit (READ
-- COMMITTED, the count a later statement); the insert's foreign key then
-- takes KEY SHARE on the epic's row, which epic archive's FOR NO KEY UPDATE
-- does not block. SECURITY INVOKER: the caller's tenant policy applies.
CREATE FUNCTION chat_epic_notice(p_epic_id text, p_kind text, p_trigger_task_id text) RETURNS void
LANGUAGE plpgsql SECURITY INVOKER AS $$
DECLARE
  v_dest jsonb;
  v_all integer;
  v_done integer;
  v_failed integer;
  v_aborted integer;
  v_detail text := '';
  v_ms bigint := (extract(epoch FROM clock_timestamp()) * 1000)::bigint;
  v_b36 text := '';
BEGIN
  SELECT COALESCE(CASE WHEN e.origin ? 'conversationId' THEN e.origin END, e.origin->'notify',
           (SELECT s.origin FROM sessions s WHERE e.origin->>'via' = 'session' AND s.id = e.origin->>'from'))
    INTO v_dest FROM epics e WHERE e.id = p_epic_id;
  IF v_dest->>'provider' IS NULL OR v_dest->>'conversationId' IS NULL THEN
    RETURN;
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('epic_notices:' || p_epic_id));
  IF p_kind = 'epic_finished' THEN
    SELECT count(*), count(*) FILTER (WHERE t.status = 'done'), count(*) FILTER (WHERE t.status = 'failed'),
           count(*) FILTER (WHERE t.status = 'aborted')
      INTO v_all, v_done, v_failed, v_aborted
      FROM tasks t WHERE t.epic_id = p_epic_id AND t.archived_at IS NULL;
    IF v_all = 0 OR v_done + v_failed + v_aborted <> v_all THEN
      RETURN;
    END IF;
    v_detail := format('%s done, %s failed, %s cancelled', v_done, v_failed, v_aborted);
  END IF;
  -- newId("chatNotice")'s shape: base-36 milliseconds, 9 wide, then 16 hex.
  WHILE v_ms > 0 LOOP
    v_b36 := substr('0123456789abcdefghijklmnopqrstuvwxyz', (v_ms % 36)::int + 1, 1) || v_b36;
    v_ms := v_ms / 36;
  END LOOP;
  -- A direct message's top-level mention is answered at the top level.
  INSERT INTO chat_notices (id, organization_id, epic_id, trigger_task_id, kind, provider, external_workspace_id,
                            conversation_id, conversation_name, thread_id, subject_key, subject_title, detail, next_attempt_at)
  SELECT 'chn_' || lpad(v_b36, 9, '0') || substr(replace(gen_random_uuid()::text, '-', ''), 1, 16),
         e.organization_id, e.id,
         -- Only a task of the epic's organisation; a foreign or unknown id is stored as NULL.
         (SELECT t.id FROM tasks t WHERE t.id = p_trigger_task_id AND t.organization_id = e.organization_id),
         p_kind, v_dest->>'provider', COALESCE(v_dest->>'workspaceId', ''),
         v_dest->>'conversationId', COALESCE(v_dest->>'conversationName', ''),
         CASE WHEN v_dest->>'conversationKind' = 'dm' AND COALESCE((v_dest->>'topLevel')::boolean, false) THEN ''
              ELSE COALESCE(v_dest->>'threadId', '') END,
         p.key_prefix, e.title, v_detail, clock_timestamp()
  FROM epics e JOIN projects p ON p.id = e.project_id
  WHERE e.id = p_epic_id
  ON CONFLICT DO NOTHING;
END $$;
