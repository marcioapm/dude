-- 176_guide_sections.sql — dude's own guide (orchestrator/internal/guide),
-- indexed for search_memory's type doc.
--
-- One row per section of a built-in guide page, shared by every
-- organisation: the guide is dude's text, not a tenant's, so the table has
-- no organization_id and no row-level security. The orchestrator writes it
-- at start (guide.Sync, as dude_sweeper) from the pages its build embeds.
-- Its words are indexed as search_documents' are,
-- and its meaning when the indexer embeds it.
CREATE TABLE guide_sections (
  -- <page>#<anchor>; the intro's anchor is "".
  id                text PRIMARY KEY,
  page              text NOT NULL,
  anchor            text NOT NULL,
  title             text NOT NULL,
  body              text NOT NULL,
  content_hash      text NOT NULL,
  -- The build that last wrote the row's text.
  version           text NOT NULL,
  tsv               tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('english', title), 'A')
      || setweight(to_tsvector('english', body), 'B')) STORED,
  -- The same size as search_documents.embedding: one embedder for both.
  embedding         halfvec(768),
  embedding_model   text,
  embedded_at       timestamptz,
  attempts          integer NOT NULL DEFAULT 0,
  last_error        text,
  last_attempt_at   timestamptz,
  next_attempt_at   timestamptz NOT NULL DEFAULT '-infinity',
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (page, anchor)
);
CREATE INDEX guide_sections_tsv_idx ON guide_sections USING gin (tsv);
CREATE INDEX guide_sections_embedding_idx ON guide_sections USING hnsw (embedding halfvec_cosine_ops);
CREATE INDEX guide_sections_pending_idx ON guide_sections (next_attempt_at) WHERE embedding IS NULL;

-- New tables are dude_app's to write by default (002); this one is not
-- granted to it beyond SELECT. dude_app is a member of dude_sweeper (008)
-- and inherits its grants, so the rule that only Sync writes the table is
-- the code's, not the catalogue's.
REVOKE ALL ON guide_sections FROM dude_app;
GRANT SELECT ON guide_sections TO dude_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON guide_sections TO dude_sweeper;
