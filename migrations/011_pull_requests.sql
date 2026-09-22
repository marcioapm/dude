-- Pull requests: the factory's output, and the state it waits on.
--
-- A Run produces commits; a pull request is how those commits become part of
-- the repository. Tracked here rather than read from the forge on demand so a
-- Run's outcome survives the forge being slow, rate-limited or unreachable —
-- and so the ledger has something stable to reference.

CREATE TYPE pull_request_state AS ENUM (
  'draft', 'open', 'merged', 'closed'
);

-- What the forge's checks say. Deliberately coarser than any one forge's
-- vocabulary: the question a human asks is "can this merge", not which of
-- GitHub's twelve conclusion values applies.
CREATE TYPE check_state AS ENUM (
  'pending', 'passing', 'failing', 'unknown'
);

CREATE TYPE review_state AS ENUM (
  'pending', 'approved', 'changes_requested'
);

CREATE TABLE pull_requests (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id      text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  work_item_id    text NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
  -- The Run whose commits opened it. Kept when the Run is deleted: the PR
  -- outlives the attempt that produced it.
  run_id          text REFERENCES runs(id) ON DELETE SET NULL,
  repository_id   text NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,

  -- The forge's own identifiers. `number` is what a human quotes; `node_id`
  -- is what the API wants back.
  number          integer NOT NULL,
  node_id         text,
  url             text NOT NULL,

  head_branch     text NOT NULL,
  base_branch     text NOT NULL,
  head_sha        text NOT NULL,

  title           text NOT NULL,
  body            text NOT NULL DEFAULT '',

  state           pull_request_state NOT NULL DEFAULT 'open',
  checks          check_state NOT NULL DEFAULT 'pending',
  review          review_state NOT NULL DEFAULT 'pending',

  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  merged_at       timestamptz,
  closed_at       timestamptz,

  -- One PR per branch per repository: reopening the same branch updates the
  -- existing row rather than accumulating duplicates.
  UNIQUE (repository_id, number)
);

CREATE INDEX pull_requests_work_item_idx ON pull_requests (work_item_id);
CREATE INDEX pull_requests_run_idx ON pull_requests (run_id);
-- The "what is waiting on me" query: open PRs whose checks or review are not
-- yet settled.
CREATE INDEX pull_requests_open_idx ON pull_requests (organization_id, state)
  WHERE state IN ('draft', 'open');

ALTER TABLE pull_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE pull_requests FORCE ROW LEVEL SECURITY;

CREATE POLICY pull_requests_isolation ON pull_requests
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));

GRANT SELECT, INSERT, UPDATE ON pull_requests TO dude_app;

-- ---------------------------------------------------------------------------
-- Forge credentials
-- ---------------------------------------------------------------------------

-- How an organization authenticates to a git forge.
--
-- `pat` is a long-lived personal access token: enough to prove the loop works,
-- and deliberately the weaker option. `github_app` mints a short-lived
-- installation token per operation, which is what a real deployment uses
-- (plan §38) — the row shape is the same so swapping one for the other does
-- not touch anything above this table.
CREATE TYPE forge_kind AS ENUM ('github');
CREATE TYPE forge_auth_kind AS ENUM ('pat', 'github_app');

CREATE TABLE forge_credentials (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  forge           forge_kind NOT NULL DEFAULT 'github',
  auth            forge_auth_kind NOT NULL,

  -- For `pat`: the token. For `github_app`: the private key.
  -- Encrypted at rest is a later concern; for now the column is never
  -- selected by any route that returns data to a client.
  secret          text NOT NULL,
  -- For `github_app` only.
  app_id          text,
  installation_id text,

  -- API root, so GitHub Enterprise works without a code change.
  api_base_url    text NOT NULL DEFAULT 'https://api.github.com',

  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  -- One credential per forge per organization, for now.
  UNIQUE (organization_id, forge)
);

ALTER TABLE forge_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE forge_credentials FORCE ROW LEVEL SECURITY;

CREATE POLICY forge_credentials_isolation ON forge_credentials
  USING (organization_id = current_setting('app.organization_id', true))
  WITH CHECK (organization_id = current_setting('app.organization_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON forge_credentials TO dude_app;
