-- 063_index_due_on_any_clock.sql — new work is due whatever the clocks say.
--
-- The indexer takes a document when next_attempt_at <= the orchestrator's
-- clock, but a new document, one whose words changed, and every document
-- a Reindex or a change of model cleared were stamped with the database's
-- now(). A database clock ahead of the orchestrator's (Docker on a Mac runs
-- tens of milliseconds ahead) held that work back by the difference. They
-- are stamped -infinity now: due at once on any clock, and still found by
-- the pending index's range scan. Only a refusal's backoff is a real time,
-- and the indexer stamps that on its own clock.

ALTER TABLE search_documents ALTER COLUMN next_attempt_at SET DEFAULT '-infinity';

CREATE OR REPLACE FUNCTION search_document_put(
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
    next_attempt_at = CASE WHEN d.content_hash = EXCLUDED.content_hash THEN d.next_attempt_at ELSE '-infinity' END;
END $$;

-- What is waiting and was never refused is due at once.
UPDATE search_documents SET next_attempt_at = '-infinity' WHERE embedding IS NULL AND attempts = 0;
