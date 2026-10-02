-- 066_attachments.sql — images a person sends an agent.
--
-- A steer, an answer or a task's first prompt may carry images. Each is two
-- objects in the photo bucket (DUDE_S3_BUCKET): the original as picked, and
-- the variant the agent is sent (scaled to at most 2000 px, re-encoded, so
-- one message's images fit lux's input). The row keeps their keys and what
-- each is; the bytes are only in storage.
--
-- An upload is made before its message is sent, and attached when it is: to
-- a directive (a steer, or an answer: answers are directives), or to the
-- task's prompt. One left unattached for a day is swept, as are a task's
-- when the task row goes (ON DELETE CASCADE); either way its objects are
-- queued for deletion by a trigger, and the backend's sweeper deletes them.

CREATE TABLE attachments (
  id                      text PRIMARY KEY,
  organization_id         text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id                 text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  uploaded_by             text REFERENCES people(id) ON DELETE SET NULL,
  -- Shown, and passed to lux: lux's rule (1..255 bytes, no path separators,
  -- NUL or control characters).
  name                    text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 255 AND name !~ '[/\\[:cntrl:]]'),

  -- The variant the agent gets.
  content_type            text NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp', 'image/gif')),
  width                   integer NOT NULL CHECK (width > 0),
  height                  integer NOT NULL CHECK (height > 0),
  bytes                   integer NOT NULL CHECK (bytes > 0),
  sha256                  text NOT NULL,
  object_key              text NOT NULL,

  -- The image as the person picked it.
  original_content_type   text NOT NULL CHECK (original_content_type IN ('image/png', 'image/jpeg', 'image/webp', 'image/gif')),
  original_width          integer NOT NULL CHECK (original_width > 0),
  original_height         integer NOT NULL CHECK (original_height > 0),
  original_bytes          integer NOT NULL CHECK (original_bytes > 0),
  original_key            text NOT NULL,

  -- What it is attached to: a directive, or the task's first prompt.
  -- Neither: uploaded, not sent yet.
  directive_id            text REFERENCES directives(id) ON DELETE CASCADE,
  for_prompt              boolean NOT NULL DEFAULT false,
  -- Its place among its message's images.
  position                integer NOT NULL DEFAULT 0,
  attached_at             timestamptz,
  created_at              timestamptz NOT NULL DEFAULT now(),
  CHECK (NOT (directive_id IS NOT NULL AND for_prompt)),
  CHECK ((directive_id IS NOT NULL OR for_prompt) = (attached_at IS NOT NULL))
);

CREATE INDEX attachments_task_idx ON attachments (task_id);
CREATE INDEX attachments_directive_idx ON attachments (directive_id) WHERE directive_id IS NOT NULL;
-- The sweep of uploads never sent.
CREATE INDEX attachments_unattached_idx ON attachments (created_at) WHERE attached_at IS NULL;

ALTER TABLE attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE attachments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON attachments
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON attachments TO dude_app;
-- The sweeper removes uploads never sent, across organizations.
GRANT SELECT, DELETE ON attachments TO dude_sweeper;

-- Objects whose row is gone, to delete from storage. Only keys: the
-- sweeper drains it and needs nothing else of a tenant.
CREATE TABLE attachment_object_deletions (
  object_key      text PRIMARY KEY,
  organization_id text NOT NULL,
  queued_at       timestamptz NOT NULL DEFAULT now()
);
-- The sweeper drains oldest first.
CREATE INDEX attachment_object_deletions_queued_idx ON attachment_object_deletions (queued_at);
GRANT SELECT, DELETE ON attachment_object_deletions TO dude_sweeper;

-- Every way a row goes (the task's cascade, the sweep, a person removing
-- an unsent one) queues its objects. SECURITY DEFINER: the deleting role
-- may not write the queue itself.
CREATE FUNCTION attachment_objects_deleted() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  INSERT INTO attachment_object_deletions (object_key, organization_id)
  VALUES (OLD.object_key, OLD.organization_id), (OLD.original_key, OLD.organization_id)
  ON CONFLICT (object_key) DO NOTHING;
  RETURN OLD;
END $$;
REVOKE ALL ON FUNCTION attachment_objects_deleted() FROM PUBLIC;
CREATE TRIGGER attachment_objects_deleted AFTER DELETE ON attachments
  FOR EACH ROW EXECUTE FUNCTION attachment_objects_deleted();
