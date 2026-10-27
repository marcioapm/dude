-- 212_chat_link_offers.sql — "Link a project" on a chat reply: the
-- projects a brainstorm with none linked was offered, and those linked from it.

-- What the request's reply offered to link, recorded with it: its words and
-- link as posted, the session page ("Choose in dude"), and the candidate
-- projects in their order. A click names a candidate by its index here.
ALTER TABLE chat_requests ADD COLUMN link_offer jsonb CHECK (link_offer IS NULL OR jsonb_typeof(link_offer) = 'object');

-- A candidate linked from the reply: once per request and project. The
-- click's delivery is kept so its retry posts the reply's update, and
-- posted_at once it is posted.
CREATE TABLE chat_request_links (
  request_id      text NOT NULL REFERENCES chat_requests(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- The project as it read when linked, for Requests.
  project_key     text NOT NULL,
  project_name    text NOT NULL DEFAULT '',
  linked_by       text REFERENCES people(id) ON DELETE SET NULL,
  linked_by_name  text NOT NULL DEFAULT '',
  event_id        text NOT NULL,
  posted_at       timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (request_id, project_id)
);
CREATE INDEX chat_request_links_project_idx ON chat_request_links (project_id);
CREATE INDEX chat_request_links_person_idx ON chat_request_links (linked_by);
ALTER TABLE chat_request_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_request_links FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_request_links
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_request_links TO dude_app;
