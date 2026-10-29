-- Feedback belongs to a logical branch-preview server, never a task or Lux Run.
CREATE TABLE preview_feedback (
  id text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  preview_run_id text NOT NULL,
  preview_server_name text NOT NULL,
  author_person_id text NOT NULL REFERENCES people(id),
  submission_key text NOT NULL CHECK (length(submission_key) BETWEEN 1 AND 200),
  body text NOT NULL CHECK (length(body) BETWEEN 1 AND 16000),
  anchor jsonb NOT NULL CHECK (anchor->>'kind' = 'preview' AND pg_column_size(anchor) <= 65536),
  capture_sha text CHECK (capture_sha ~ '^[0-9a-f]{40,64}$'),
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
  resolved_at timestamptz,
  resolved_by text REFERENCES people(id),
  resolution_note text NOT NULL DEFAULT '' CHECK (length(resolution_note) <= 2000),
  history jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY (preview_run_id, preview_server_name) REFERENCES preview_servers(run_id, name) ON DELETE CASCADE,
  UNIQUE (preview_run_id, preview_server_name, author_person_id, submission_key),
  CHECK ((status = 'resolved') = (resolved_at IS NOT NULL)),
  CHECK ((status = 'resolved') = (resolved_by IS NOT NULL))
);
CREATE INDEX preview_feedback_list_idx ON preview_feedback (preview_run_id, id);
CREATE INDEX preview_feedback_status_idx ON preview_feedback (preview_run_id, status, id);
CREATE INDEX preview_feedback_server_status_idx ON preview_feedback (preview_run_id, preview_server_name, status, id);
CREATE INDEX preview_feedback_server_idx ON preview_feedback (preview_run_id, preview_server_name, id);
CREATE INDEX preview_feedback_attachment_idx ON preview_feedback ((anchor->>'attachmentId'));
ALTER TABLE preview_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE preview_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON preview_feedback
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON preview_feedback TO dude_app;
GRANT SELECT (anchor) ON preview_feedback TO dude_sweeper;

-- Reuse image validation, serving and the durable object-deletion queue.
ALTER TABLE attachments ADD COLUMN preview_run_id text,
  ADD COLUMN preview_server_name text,
  ADD CONSTRAINT attachments_preview_server_fk FOREIGN KEY (preview_run_id, preview_server_name)
    REFERENCES preview_servers(run_id, name) ON DELETE CASCADE;
ALTER TABLE attachments DROP CONSTRAINT attachments_one_owner;
ALTER TABLE attachments ADD CONSTRAINT attachments_one_owner
  CHECK (num_nonnulls(task_id, session_id, preview_run_id) = 1
    AND (preview_run_id IS NULL) = (preview_server_name IS NULL));
ALTER TABLE attachments ADD CONSTRAINT attachments_preview_not_message
  CHECK (preview_run_id IS NULL OR (NOT for_prompt AND directive_id IS NULL));
CREATE INDEX attachments_preview_idx ON attachments (preview_run_id, preview_server_name)
  WHERE preview_run_id IS NOT NULL;

CREATE FUNCTION queue_preview_upload_cleanup(delivered text, original text) RETURNS void
  LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
  INSERT INTO attachment_object_deletions (object_key, organization_id)
    SELECT k, current_organization_id() FROM unnest(ARRAY[delivered, original]) k
    WHERE starts_with(k, 'attachments/' || current_organization_id() || '/')
    ON CONFLICT (object_key) DO NOTHING;
$$;
REVOKE ALL ON FUNCTION queue_preview_upload_cleanup(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION queue_preview_upload_cleanup(text, text) TO dude_app;

-- Run first, then server, matching wake and feed lifecycle transactions.
CREATE FUNCTION require_live_feedback_server() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_project text;
BEGIN
  IF NEW.preview_run_id IS NULL THEN RETURN NEW; END IF;
  SELECT r.project_id INTO owner_project FROM runs r WHERE r.id = NEW.preview_run_id
    AND r.organization_id = NEW.organization_id AND r.preview_branch IS NOT NULL
    AND r.kind = 'preview' AND r.status IN ('pending', 'scheduled', 'starting', 'running', 'paused') FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'preview server not found' USING ERRCODE = '23503'; END IF;
  PERFORM 1 FROM preview_servers s WHERE s.run_id = NEW.preview_run_id
    AND s.name = NEW.preview_server_name AND s.organization_id = NEW.organization_id
    AND s.deleted_at IS NULL FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'preview server not found' USING ERRCODE = '23503'; END IF;
  IF TG_TABLE_NAME = 'preview_feedback' THEN
    IF NEW.project_id <> owner_project OR NOT EXISTS (SELECT 1 FROM people p
      WHERE p.id = NEW.author_person_id AND p.organization_id = NEW.organization_id
        AND (TG_OP <> 'INSERT' OR p.removed_at IS NULL))
      OR (NEW.resolved_by IS NOT NULL AND NOT EXISTS (SELECT 1 FROM people p
        WHERE p.id = NEW.resolved_by AND p.organization_id = NEW.organization_id AND p.removed_at IS NULL)) THEN
      RAISE EXCEPTION 'preview owner mismatch' USING ERRCODE = '23503';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER preview_feedback_live BEFORE INSERT OR UPDATE ON preview_feedback
  FOR EACH ROW EXECUTE FUNCTION require_live_feedback_server();
CREATE TRIGGER attachments_preview_live BEFORE INSERT OR UPDATE ON attachments
  FOR EACH ROW WHEN (NEW.preview_run_id IS NOT NULL) EXECUTE FUNCTION require_live_feedback_server();

CREATE FUNCTION purge_ended_preview_feedback() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM preview_feedback WHERE preview_run_id = NEW.id;
  DELETE FROM attachments WHERE preview_run_id = NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER runs_purge_preview_feedback AFTER UPDATE OF status ON runs
  FOR EACH ROW WHEN (NEW.preview_branch IS NOT NULL AND NEW.status IN ('completed', 'failed', 'aborted')
    AND OLD.status IS DISTINCT FROM NEW.status) EXECUTE FUNCTION purge_ended_preview_feedback();

CREATE FUNCTION purge_deleted_server_feedback() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM preview_feedback WHERE preview_run_id = NEW.run_id AND preview_server_name = NEW.name;
  DELETE FROM attachments WHERE preview_run_id = NEW.run_id AND preview_server_name = NEW.name;
  RETURN NULL;
END $$;
CREATE TRIGGER preview_servers_purge_feedback AFTER UPDATE OF deleted_at ON preview_servers
  FOR EACH ROW WHEN (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL)
  EXECUTE FUNCTION purge_deleted_server_feedback();
