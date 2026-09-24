-- A work item names the repositories it touches: none, one, or several.
--
-- `write`: the agents may change it, and a change becomes a pull request in
-- it. `read`: cloned for context, never pushed. A work item across two
-- repositories is one work item with a pull request in each it changed
-- (docs/design/multi-repo.md), which replaces the single
-- work_items.repository_id.

-- These read and rewrite every organization's rows, which only a role that
-- bypasses row-level security can see: refuse outright rather than migrate
-- nothing and then drop the column.
DO $$
BEGIN
  IF NOT (SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user) THEN
    RAISE EXCEPTION 'migration 018 must run as a role that bypasses row-level security (it moves every organization''s data)';
  END IF;
END $$;

CREATE TYPE repository_access AS ENUM ('write', 'read');

CREATE TABLE work_item_repositories (
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  work_item_id    text NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  repository_id   text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  access          repository_access NOT NULL DEFAULT 'write',
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (work_item_id, repository_id)
);

CREATE INDEX work_item_repositories_repository_idx ON work_item_repositories (repository_id);

ALTER TABLE work_item_repositories ENABLE ROW LEVEL SECURITY;
ALTER TABLE work_item_repositories FORCE ROW LEVEL SECURITY;

CREATE POLICY work_item_repositories_isolation ON work_item_repositories
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON work_item_repositories TO dude_app;
-- The phase notifier looks across organizations to find finished Runs, and
-- whether their work changes code decides whether they wait for artifacts.
GRANT SELECT ON work_item_repositories TO dude_sweeper;

INSERT INTO work_item_repositories (organization_id, work_item_id, repository_id, access)
SELECT organization_id, id, repository_id, 'write' FROM work_items WHERE repository_id IS NOT NULL;

-- A work item that named none used its project's only repository. Say so,
-- as every new one does when it is created (or delivered) in a project with
-- one: from here on, a work item naming none changes no code.

INSERT INTO work_item_repositories (organization_id, work_item_id, repository_id, access)
SELECT w.organization_id, w.id, (SELECT r.id FROM repositories r WHERE r.project_id = w.project_id), 'write'
FROM work_items w
WHERE w.repository_id IS NULL
  AND (SELECT count(*) FROM repositories r WHERE r.project_id = w.project_id) = 1;

ALTER TABLE work_items DROP COLUMN repository_id;

-- A phase Run works on every repository its work item names, each from its
-- own commit: the commit per repository it started from, and what it left
-- in each it changed. Keyed by repository name, as lux reports pushes.
ALTER TABLE runs
  -- {name: sha}; empty for the first phase, which starts from each default
  -- branch.
  ADD COLUMN base_refs jsonb NOT NULL DEFAULT '{}',
  -- {name: sha}: what each checkout actually started from, as lux reported
  -- it — the base a change is measured against.
  ADD COLUMN base_shas jsonb NOT NULL DEFAULT '{}',
  -- Where the Run's work is pushed, decided when it is submitted; NULL for
  -- a phase that pushes nothing (a reviewer, or work on no repository it
  -- may change).
  ADD COLUMN push_branch text,
  -- {name: {sha, changedPaths}} for each repository the Run changed.
  ADD COLUMN heads     jsonb NOT NULL DEFAULT '{}';

-- Runs and deliveries from before carry on: each held one repository, so
-- what it held becomes that repository's entry. A Run's repository is its
-- own column or, failing that, its work item's.
WITH named AS (
  SELECT r.id, repo.name, repo.id AS repository_id
  FROM runs r
  JOIN work_item_repositories wr ON wr.work_item_id = r.work_item_id
  JOIN repositories repo ON repo.id = COALESCE(r.repository_id, wr.repository_id)
  WHERE r.phase IS NOT NULL
)
UPDATE runs r SET
  base_refs = CASE WHEN r.base_ref IS NOT NULL THEN jsonb_build_object(n.name, r.base_ref) ELSE '{}' END,
  base_shas = CASE WHEN r.base_sha IS NOT NULL THEN jsonb_build_object(n.name, r.base_sha) ELSE '{}' END,
  heads = CASE WHEN r.head_sha IS NOT NULL AND cardinality(r.changed_paths) > 0
               THEN jsonb_build_object(n.name, jsonb_build_object('sha', r.head_sha, 'changedPaths', to_jsonb(r.changed_paths)))
               ELSE '{}' END,
  -- Still to push: where it would have pushed.
  push_branch = CASE WHEN r.phase IN ('implement', 'fix', 'simplify') AND r.lux_run_id IS NOT NULL
                     THEN 'dude/' || r.work_item_id || '/run-' || r.id END
FROM named n WHERE n.id = r.id;

-- A delivery's state names its repository's head by name now, and its pull
-- requests as a list.
UPDATE workflow_runs w SET state = (w.state - 'repositoryId' - 'headSha' - 'pullRequestId')
  || CASE WHEN w.state ? 'headSha' AND repo.name IS NOT NULL
          THEN jsonb_build_object('heads', jsonb_build_object(repo.name, w.state->>'headSha')) ELSE '{}' END
  || CASE WHEN w.state ? 'pullRequestId'
          THEN jsonb_build_object('pullRequestIds', jsonb_build_array(w.state->>'pullRequestId')) ELSE '{}' END
FROM workflow_runs w2 LEFT JOIN repositories repo ON repo.id = w2.state->>'repositoryId'
WHERE w2.id = w.id AND w.state ? 'repositoryId';

-- What a Run held for one repository, now held per repository above.
DROP INDEX IF EXISTS runs_repository_idx;
ALTER TABLE runs
  DROP COLUMN base_ref,
  DROP COLUMN base_sha,
  DROP COLUMN head_sha,
  DROP COLUMN changed_paths,
  DROP COLUMN repository_id;
