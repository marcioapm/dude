-- Memory: what people, dude and agents remember, and one index over it and
-- the work (docs/design/memory.md).
--
-- A memory is live when it is saved; archiving takes it out of search.
-- search_documents is derived: one row per memory, task, epic and project,
-- kept in step by the triggers below and rebuildable from them. Its words
-- are indexed at once (tsv); its meaning when the orchestrator's indexer
-- embeds it. A change of text clears the embedding, an unchanged one keeps
-- it, so saving a task's status does not cost an embedding.
--
-- pgvector is not a trusted extension: creating it needs a superuser. An
-- owner that is not one (a managed Postgres) needs it created beforehand,
-- once, by one that is: CREATE EXTENSION vector; (docs/operations.md).
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS vector;
EXCEPTION WHEN insufficient_privilege THEN
  RAISE EXCEPTION 'memory needs the pgvector extension, and this role may not create it'
    USING HINT = 'As a superuser, in this database, once: CREATE EXTENSION vector; then migrate again. See docs/operations.md, Postgres.';
END $$;

CREATE TABLE memories (
  id                text PRIMARY KEY,
  organization_id   text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- Null: the whole organization, and every project's search finds it.
  project_id        text REFERENCES projects(id) ON DELETE CASCADE,
  title             text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  content           text NOT NULL CHECK (length(content) <= 20000),
  kind              text NOT NULL DEFAULT 'fact' CHECK (kind IN ('fact', 'procedure', 'note')),
  -- Who: a person, dude itself (an automation, saying why), or an agent's
  -- Run — which names the person it worked for and its task.
  author_kind       text NOT NULL CHECK (author_kind IN ('person', 'system', 'agent')),
  author_person_id  text REFERENCES people(id) ON DELETE SET NULL,
  created_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  system_reason     text,
  -- Where it was learned, when it was: a task, epic, project or run.
  source_type       text CHECK (source_type IN ('task', 'epic', 'project', 'run')),
  source_id         text,
  archived_at       timestamptz,
  archived_by       text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CHECK ((source_type IS NULL) = (source_id IS NULL))
);
CREATE INDEX memories_org_idx ON memories (organization_id, created_at DESC);
CREATE INDEX memories_project_idx ON memories (project_id) WHERE project_id IS NOT NULL;
CREATE TRIGGER memories_updated_at BEFORE UPDATE ON memories
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- What a memory is about, as many as it concerns. Plain ids, not foreign
-- keys: a task deleted leaves its mention, which search ignores.
CREATE TABLE memory_refs (
  memory_id       text NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  ref_type        text NOT NULL CHECK (ref_type IN ('task', 'epic', 'project')),
  ref_id          text NOT NULL,
  PRIMARY KEY (memory_id, ref_type, ref_id)
);
CREATE INDEX memory_refs_ref_idx ON memory_refs (ref_type, ref_id);

CREATE TABLE search_documents (
  source_type       text NOT NULL CHECK (source_type IN ('memory', 'task', 'epic', 'project')),
  source_id         text NOT NULL,
  organization_id   text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  -- Null only for an organization's memories.
  project_id        text REFERENCES projects(id) ON DELETE CASCADE,
  title             text NOT NULL,
  body              text NOT NULL,
  content_hash      text NOT NULL,
  tsv               tsvector NOT NULL,
  -- 768 is DUDE_EMBEDDINGS_DIMENSIONS; another size is another migration.
  embedding         halfvec(768),
  embedding_model   text,
  embedded_at       timestamptz,
  -- The indexer's: how often it failed, why, and when it tries again.
  attempts          integer NOT NULL DEFAULT 0,
  last_error        text,
  last_attempt_at   timestamptz,
  next_attempt_at   timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_type, source_id)
);
CREATE INDEX search_documents_tsv_idx ON search_documents USING gin (tsv);
CREATE INDEX search_documents_embedding_idx ON search_documents USING hnsw (embedding halfvec_cosine_ops);
CREATE INDEX search_documents_scope_idx ON search_documents (organization_id, project_id);
-- What the indexer takes next, across organizations: a change of model
-- clears every embedding (Reindex), so "pending" is always "no embedding".
CREATE INDEX search_documents_pending_idx ON search_documents (next_attempt_at) WHERE embedding IS NULL;

ALTER TABLE memories ENABLE ROW LEVEL SECURITY;
ALTER TABLE memories FORCE ROW LEVEL SECURITY;
CREATE POLICY memories_isolation ON memories
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
ALTER TABLE memory_refs ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_refs FORCE ROW LEVEL SECURITY;
CREATE POLICY memory_refs_isolation ON memory_refs
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));
ALTER TABLE search_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE search_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY search_documents_isolation ON search_documents
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));

GRANT SELECT, INSERT, UPDATE ON memories TO dude_app;
GRANT SELECT, INSERT, DELETE ON memory_refs TO dude_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON search_documents TO dude_app;
-- The indexer embeds across organizations: it reads what is pending and
-- writes back the embedding, and touches nothing else.
GRANT SELECT, UPDATE ON search_documents TO dude_sweeper;

-- ---------------------------------------------------------------------------
-- Keeping the index in step. The triggers run as whoever wrote the row, so
-- the index's row-level security holds for them as for any write: a
-- tenant's write can only ever touch its own organization's index. (No
-- SECURITY DEFINER: that would let a caller name any organization.)
-- ---------------------------------------------------------------------------

CREATE FUNCTION search_document_put(
  p_type text, p_id text, p_org text, p_project text, p_title text, p_body text
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  h text := md5(p_title || E'\n' || p_body);
BEGIN
  INSERT INTO search_documents AS d (source_type, source_id, organization_id, project_id, title, body, content_hash, tsv)
  VALUES (p_type, p_id, p_org, p_project, p_title, p_body, h,
          setweight(to_tsvector('simple', p_title), 'A') || setweight(to_tsvector('english', p_title), 'A')
            || setweight(to_tsvector('english', p_body), 'B'))
  ON CONFLICT (source_type, source_id) DO UPDATE SET
    project_id = EXCLUDED.project_id,
    title = EXCLUDED.title,
    body = EXCLUDED.body,
    content_hash = EXCLUDED.content_hash,
    tsv = EXCLUDED.tsv,
    updated_at = now(),
    -- New words need a new embedding, now; the same words keep theirs.
    embedding = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.embedding END,
    embedding_model = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.embedding_model END,
    embedded_at = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.embedded_at END,
    attempts = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.attempts ELSE 0 END,
    last_error = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.last_error END,
    last_attempt_at = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.last_attempt_at END,
    next_attempt_at = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.next_attempt_at ELSE now() END;
END $$;

CREATE FUNCTION search_document_drop(p_type text, p_id text) RETURNS void
LANGUAGE sql AS $$
  DELETE FROM search_documents WHERE source_type = p_type AND source_id = p_id;
$$;

-- A task's document: its key as people say it (TEXT-12) and title; its goal
-- and each of its criteria. One place, for the trigger, a new key prefix
-- and the backfill, none of which may touch the task itself (its
-- updated_at is its last activity).
CREATE FUNCTION search_document_put_task(p_task text) RETURNS void LANGUAGE sql AS $$
  SELECT search_document_put('task', t.id, t.organization_id, t.project_id,
    p.key_prefix || '-' || t.number || ' ' || t.title,
    concat_ws(E'\n', nullif(t.goal, ''), nullif((
      SELECT string_agg(c, E'\n') FROM jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(t.acceptance_criteria) = 'array' THEN t.acceptance_criteria ELSE '[]' END) c), '')))
  FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = p_task;
$$;

CREATE FUNCTION memories_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM search_document_drop('memory', OLD.id);
  ELSIF NEW.archived_at IS NOT NULL THEN
    PERFORM search_document_drop('memory', NEW.id);
  ELSE
    PERFORM search_document_put('memory', NEW.id, NEW.organization_id, NEW.project_id, NEW.title, NEW.content);
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER memories_index AFTER INSERT OR UPDATE OF title, content, project_id, archived_at OR DELETE ON memories
  FOR EACH ROW EXECUTE FUNCTION memories_index();

CREATE FUNCTION tasks_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM search_document_drop('task', OLD.id);
  ELSE
    PERFORM search_document_put_task(NEW.id);
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER tasks_index AFTER INSERT OR UPDATE OF title, goal, acceptance_criteria, number, project_id OR DELETE ON tasks
  FOR EACH ROW EXECUTE FUNCTION tasks_index();

CREATE FUNCTION epics_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM search_document_drop('epic', OLD.id);
  ELSE
    PERFORM search_document_put('epic', NEW.id, NEW.organization_id, NEW.project_id, NEW.title, NEW.description);
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER epics_index AFTER INSERT OR UPDATE OF title, description, project_id OR DELETE ON epics
  FOR EACH ROW EXECUTE FUNCTION epics_index();

CREATE FUNCTION projects_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM search_document_drop('project', OLD.id);
  ELSE
    PERFORM search_document_put('project', NEW.id, NEW.organization_id, NEW.id, NEW.name, NEW.description);
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER projects_index AFTER INSERT OR UPDATE OF name, description OR DELETE ON projects
  FOR EACH ROW EXECUTE FUNCTION projects_index();

-- A project's new key prefix renames every task's key, in the index only.
CREATE FUNCTION projects_reindex_tasks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM search_document_put_task(t.id) FROM tasks t WHERE t.project_id = NEW.id;
  RETURN NULL;
END $$;
CREATE TRIGGER projects_reindex_tasks AFTER UPDATE OF key_prefix ON projects
  FOR EACH ROW WHEN (OLD.key_prefix IS DISTINCT FROM NEW.key_prefix)
  EXECUTE FUNCTION projects_reindex_tasks();

-- What exists today, indexed once.
SELECT search_document_put('project', p.id, p.organization_id, p.id, p.name, p.description) FROM projects p;
SELECT search_document_put('epic', e.id, e.organization_id, e.project_id, e.title, e.description) FROM epics e;
SELECT search_document_put_task(t.id) FROM tasks t;
