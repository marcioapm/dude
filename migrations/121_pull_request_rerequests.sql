-- 121_pull_request_rerequests.sql — after dude pushes a fix to an open pull
-- request, it asks the reviewers of the older head again, once per pull
-- request and head (delivery.RerequestReviews).
--
-- One row per (task, repository, number, head), inserted before GitHub is
-- contacted: the caller whose INSERT lands is the only one that asks for
-- that head. `token` fences its holder: the outcome and its event are
-- recorded only under the token that holds the row. A holder that failed
-- releases the row (claimed_at '-infinity'); one that crashed lapses after
-- the holder's bound; either is taken over by writing a new token.
-- `asking` is who the holder is about to ask, written before its POST: a
-- takeover records whichever of them GitHub now holds as requested instead
-- of asking them again. `outcome` is the event's payload, NULL when nobody
-- was asked.
--
-- A new table only: nothing existing is rewritten or locked.

CREATE TABLE pull_request_rerequests (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id         text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  repo            text NOT NULL,
  number          integer NOT NULL,
  head            text NOT NULL,
  token           text NOT NULL,
  claimed_at      timestamptz NOT NULL DEFAULT now(),
  asking          text[],
  outcome         jsonb,
  finished_at     timestamptz,
  PRIMARY KEY (task_id, repo, number, head)
);

ALTER TABLE pull_request_rerequests ENABLE ROW LEVEL SECURITY;
ALTER TABLE pull_request_rerequests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pull_request_rerequests
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON pull_request_rerequests TO dude_app;
