-- 127_feedback.sql — a person's feedback on a task, kept in dude only.
--
-- A person drafts items privately (status 'draft', no batch) and submits
-- several at once as a batch. Submitted words and anchors never change: a
-- reply or a reopen continues the conversation. An item belongs to one
-- attempt of its task; a start over begins with none.
--
-- Row-level security isolates organizations only. A draft is its author's
-- alone: every read of one filters on author_person_id, and no ledger
-- event carries a draft, its id or any item's words.
--
-- 'open' and 'delivered' block the pull request and dude's merge; so does
-- a resolution naming a commit not yet verified as published
-- (resolved_commit set, resolved_verified_at NULL). 'replied' does not.

CREATE TABLE feedback_batches (
  id               text PRIMARY KEY,
  organization_id  text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id          text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  attempt          integer NOT NULL CHECK (attempt > 0),
  author_person_id text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  -- The caller's key for one submit: a retry with the same key is the same
  -- batch, never a second one.
  submission_key   text NOT NULL CHECK (length(submission_key) BETWEEN 1 AND 200),
  submitted_at     timestamptz NOT NULL DEFAULT now(),
  -- Who heard of it first: the conductor deciding, the working implementer
  -- or fixer (a directive), or the delivery's own next step.
  routed_to        text CHECK (routed_to IN ('conductor', 'steer', 'workflow')),
  routed_run_id    text REFERENCES runs(id) ON DELETE SET NULL,
  directive_id     text REFERENCES directives(id) ON DELETE SET NULL,
  UNIQUE (task_id, author_person_id, submission_key)
);

CREATE TABLE feedback_items (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  task_id               text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  attempt               integer NOT NULL CHECK (attempt > 0),
  batch_id              text REFERENCES feedback_batches(id) ON DELETE CASCADE,
  author_person_id      text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  body                  text NOT NULL CHECK (length(body) BETWEEN 1 AND 16000),
  anchor_kind           text NOT NULL CHECK (anchor_kind IN ('general', 'code', 'preview')),
  -- A code anchor's Run: its diff snapshot is what the anchor was made on.
  run_id                text REFERENCES runs(id) ON DELETE SET NULL,
  -- The anchor as the API built it (code: repo, path, side, lines, base,
  -- head when published, the snapshot's checksum and the excerpt read from
  -- it; preview: the page's URL, viewport, selector, box and capped text).
  anchor                jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (pg_column_size(anchor) <= 65536),
  status                text NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'open', 'delivered', 'resolved', 'replied')),
  -- The Run that was given it, and the directive that carried it, while delivered.
  delivered_run_id      text REFERENCES runs(id) ON DELETE SET NULL,
  directive_id          text REFERENCES directives(id) ON DELETE SET NULL,
  -- Fixer Runs sent it: bounded as a finding's fix attempts are.
  fix_attempts          integer NOT NULL DEFAULT 0,
  resolved_commit       text,
  resolved_repo         text,
  resolved_by_run_id    text REFERENCES runs(id) ON DELETE SET NULL,
  resolved_by_person_id text REFERENCES people(id) ON DELETE SET NULL,
  resolved_at           timestamptz,
  -- Set when the resolution holds: at once for a person's without a
  -- commit, when the commit is on the published head for an agent's.
  resolved_verified_at  timestamptz,
  outdated_at           timestamptz,
  submitted_at          timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CHECK ((status = 'draft') = (batch_id IS NULL)),
  CHECK ((status = 'draft') = (submitted_at IS NULL))
);

CREATE INDEX feedback_items_task_idx ON feedback_items (task_id, attempt);
CREATE INDEX feedback_items_batch_idx ON feedback_items (batch_id) WHERE batch_id IS NOT NULL;
-- The gate's query: what still blocks this task's attempt?
CREATE INDEX feedback_items_blocking_idx ON feedback_items (task_id, attempt)
  WHERE status IN ('open', 'delivered') OR (status = 'resolved' AND resolved_verified_at IS NULL);

CREATE TABLE feedback_replies (
  id              text PRIMARY KEY,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  item_id         text NOT NULL REFERENCES feedback_items(id) ON DELETE CASCADE,
  -- reply (words), resolve, reopen: the item's history, in order.
  kind            text NOT NULL CHECK (kind IN ('reply', 'resolve', 'reopen')),
  person_id       text REFERENCES people(id) ON DELETE SET NULL,
  run_id          text REFERENCES runs(id) ON DELETE SET NULL,
  body            text NOT NULL DEFAULT '' CHECK (length(body) <= 16000),
  commit_sha      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CHECK ((person_id IS NULL) <> (run_id IS NULL))
);

CREATE INDEX feedback_replies_item_idx ON feedback_replies (item_id, created_at);

-- The feedback items a Run was given, as finding_ids: a restart of the
-- phase is given the same ones.
ALTER TABLE runs ADD COLUMN feedback_item_ids text[] NOT NULL DEFAULT '{}';

-- feedback: a person submitted feedback while the conductor decides.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked', 'feedback'));

ALTER TABLE feedback_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE feedback_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feedback_batches
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON feedback_batches TO dude_app;

ALTER TABLE feedback_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE feedback_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feedback_items
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON feedback_items TO dude_app;

ALTER TABLE feedback_replies ENABLE ROW LEVEL SECURITY;
ALTER TABLE feedback_replies FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON feedback_replies
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT ON feedback_replies TO dude_app;
