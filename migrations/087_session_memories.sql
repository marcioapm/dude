-- 087_session_memories.sql — what a brainstorm session's agent remembers
-- is the session's: its accepted members read it, its own agent finds it,
-- and nobody else does through any memory read (detail, list, search,
-- index status, another agent's tools). The organisation's and projects'
-- memories are unchanged.
--
-- A session memory has no project (project_id NULL) and its session_id;
-- its search document carries the same session_id, so a search filters it
-- where it ranks.

ALTER TABLE memories ADD COLUMN session_id text REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE memories ADD CONSTRAINT memories_session_has_no_project CHECK (session_id IS NULL OR project_id IS NULL);
CREATE INDEX memories_session_idx ON memories (session_id) WHERE session_id IS NOT NULL;

ALTER TABLE search_documents ADD COLUMN session_id text REFERENCES sessions(id) ON DELETE CASCADE;
CREATE INDEX search_documents_session_idx ON search_documents (session_id) WHERE session_id IS NOT NULL;

-- Whether a memory (or its document) on p_session is readable: by its
-- session's own agent (p_agent_session), or a person who is its accepted
-- member (p_person). A memory on no session is everyone's in the
-- organisation, as before.
CREATE FUNCTION memory_visible(p_session text, p_agent_session text, p_person text) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT p_session IS NULL OR p_session = p_agent_session
    OR (p_person <> '' AND session_role(p_session, p_person) IS NOT NULL)
$$;

-- The memory's document takes its session, after search_document_put has
-- written the rest.
CREATE OR REPLACE FUNCTION memories_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    PERFORM search_document_drop('memory', OLD.id);
  ELSIF NEW.archived_at IS NOT NULL THEN
    PERFORM search_document_drop('memory', NEW.id);
  ELSE
    PERFORM search_document_put('memory', NEW.id, NEW.organization_id, NEW.project_id, NEW.title, NEW.content);
    UPDATE search_documents SET session_id = NEW.session_id
      WHERE source_type = 'memory' AND source_id = NEW.id AND session_id IS DISTINCT FROM NEW.session_id;
  END IF;
  RETURN NULL;
END $$;
