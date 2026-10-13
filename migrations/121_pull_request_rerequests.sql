-- 121_pull_request_rerequests.sql — after dude pushes a fix to an open pull
-- request, it asks the reviewers of the older head again, once per pull
-- request and head (delivery.RerequestReviews).
--
-- One row per (task, repository, number, head). Callers for one head are
-- serialized by a session advisory lock held across the whole GitHub
-- sequence; the row is what outlives a holder: written before GitHub is
-- contacted, finished with the outcome and its event in one transaction.
-- A row left unfinished (its holder's session ended) is taken over by the
-- next lock holder, which writes a new `token`; writes are fenced by it.
-- `asking` is who the holder is about to ask, written before its POST: a
-- takeover records whichever of them GitHub now holds as requested instead
-- of asking them again. `outcome` is the event's payload, NULL when nobody
-- was asked. `claimed_at` is when the current token took the row.
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
