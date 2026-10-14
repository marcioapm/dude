-- 123_task_sources.sql — a task may start from work that already exists: an
-- open pull request, or a branch, in one of its project's repositories
-- (adopted work). The source is named before the task's delivery first
-- starts and is fixed after; the delivery takes its head branch as the task
-- branch and its base as what every diff and the pull request measure
-- against.
--
-- One row per task. `number` and `title` are the pull request's for kind
-- 'pr' (title as GitHub said it when the source was attached), NULL for a
-- branch. `head_branch` is the branch dude fast-forwards; `base_branch` the
-- pull request's base, or the base a person chose for a branch.
--
-- pull_requests.adopted marks a pull request dude did not open but took
-- over from its source: never opened again, never closed on start over.
--
-- A new table and a defaulted column only: nothing existing is rewritten.

CREATE TABLE task_sources (
  task_id         text PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  repository_id   text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('pr', 'branch')),
  number          integer CHECK (number > 0),
  title           text CHECK (length(title) <= 1000),
  head_branch     text NOT NULL CHECK (length(head_branch) BETWEEN 1 AND 255),
  base_branch     text NOT NULL CHECK (length(base_branch) BETWEEN 1 AND 255),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT task_sources_number CHECK ((kind = 'pr') = (number IS NOT NULL)),
  CONSTRAINT task_sources_distinct CHECK (head_branch <> base_branch)
);
CREATE INDEX task_sources_head_idx ON task_sources (repository_id, head_branch);

ALTER TABLE task_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON task_sources
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON task_sources TO dude_app;

ALTER TABLE pull_requests ADD COLUMN adopted boolean NOT NULL DEFAULT false;
