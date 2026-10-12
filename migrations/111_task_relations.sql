-- 111_task_relations.sql — tasks that wait on each other, and a task's
-- owner asking for it to start once nothing is in front of it
-- (docs/design/task-relations.md).
--
-- A `blocks` edge A → B holds B's start until A is merged: done, with a
-- pull request in state merged. `related` gates nothing and is stored once,
-- the lower id first. Both are refused into themselves (a CHECK); a loop of
-- `blocks` is refused by the writer, which takes the organisation's
-- relation lock and asks task_relation_would_cycle first.
--
-- New tables and functions only: nothing existing is rewritten or locked
-- beyond conductor_wakes' constraint swap (a catalog change and a scan of
-- its rows, which are few and short-lived).

CREATE TYPE task_relation_kind AS ENUM ('blocks', 'related');

CREATE TABLE task_relations (
  organization_id    text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  from_task_id       text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  to_task_id         text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind               task_relation_kind NOT NULL,
  created_by_person  text REFERENCES people(id) ON DELETE SET NULL,
  created_by_run_id  text REFERENCES runs(id) ON DELETE SET NULL,
  -- A brainstorm's proposal a person filed: who proposed it, beside who filed.
  proposed_by_run_id text REFERENCES runs(id) ON DELETE SET NULL,
  why                text CHECK (length(why) <= 2000),
  created_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (from_task_id, to_task_id, kind),
  CONSTRAINT task_relations_self CHECK (from_task_id <> to_task_id),
  CONSTRAINT task_relations_related_order CHECK (kind <> 'related' OR from_task_id < to_task_id),
  CONSTRAINT task_relations_author CHECK (created_by_person IS NOT NULL OR created_by_run_id IS NOT NULL)
);
CREATE INDEX task_relations_to_idx ON task_relations (to_task_id, kind);

ALTER TABLE task_relations ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_relations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON task_relations
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON task_relations TO dude_app;
-- The auto-start worker finds the requests due across organisations: what
-- it fires is re-checked inside the organisation's own transaction.
GRANT SELECT ON task_relations TO dude_sweeper;

-- Start when unblocked: one standing request per task, its owner's.
CREATE TABLE task_auto_starts (
  task_id         text PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  how             text NOT NULL CHECK (how IN ('deliver', 'talk')),
  set_by_person   text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  set_at          timestamptz NOT NULL DEFAULT now(),
  -- Set by the merge that released the task's last blocker; the worker
  -- takes due rows, one transaction per task.
  due_at          timestamptz
);
CREATE INDEX task_auto_starts_due_idx ON task_auto_starts (due_at) WHERE due_at IS NOT NULL;

ALTER TABLE task_auto_starts ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_auto_starts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON task_auto_starts
  USING (organization_id = current_organization_id())
  WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON task_auto_starts TO dude_app;
GRANT SELECT ON task_auto_starts TO dude_sweeper;

-- A task's open blockers: every `blocks` edge into it whose blocker is not
-- merged (done, and at least one of its pull requests merged). A blocker
-- marked done with nothing merged is returned, status done: it never
-- releases anything. Invoker's rights: row-level security still applies.
-- Both processes call it (the backend's bare Run, the orchestrator's
-- RefuseBlockedTx), so the rule is written once.
CREATE FUNCTION task_blocked_by(task text) RETURNS TABLE (key text, status task_status, id text)
  LANGUAGE sql STABLE
  AS $$
    SELECT p.key_prefix || '-' || b.number, b.status, b.id
      FROM task_relations r
      JOIN tasks b ON b.id = r.from_task_id
      JOIN projects p ON p.id = b.project_id
     WHERE r.to_task_id = task AND r.kind = 'blocks'
       AND NOT (b.status = 'done'
                AND EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = b.id AND pr.state = 'merged'))
     ORDER BY b.project_id, b.number
  $$;

-- Whether a new edge `src` blocks `dst` would close a loop: the keys of the
-- loop, from dst along `blocks` edges back to src and on to dst again
-- ("CP-40 → CP-41 → CP-44 → CP-40" for CP-44 blocks CP-40), or NULL. The
-- writer holds the organisation's relation lock
-- (pg_advisory_xact_lock(hashtext('task_relations:' || org))) around this
-- and the insert, so two writers cannot each close half of one.
CREATE FUNCTION task_relation_would_cycle(src text, dst text) RETURNS text[]
  LANGUAGE sql STABLE
  AS $$
    WITH RECURSIVE walk (task, path) AS (
      SELECT dst, ARRAY[dst]
      UNION ALL
      SELECT r.to_task_id, w.path || r.to_task_id
        FROM walk w
        JOIN task_relations r ON r.from_task_id = w.task AND r.kind = 'blocks'
       WHERE NOT r.to_task_id = ANY (w.path)
    ), found AS (
      SELECT path FROM walk WHERE task = src ORDER BY array_length(path, 1) LIMIT 1
    )
    SELECT array_agg(p.key_prefix || '-' || t.number ORDER BY s.n)
      FROM found, unnest(found.path || dst) WITH ORDINALITY AS s(task, n)
      JOIN tasks t ON t.id = s.task
      JOIN projects p ON p.id = t.project_id
  $$;

-- unblocked: the task's last open blocker merged; its conductor, if one is
-- live, is told it can start.
ALTER TABLE conductor_wakes DROP CONSTRAINT conductor_wakes_kind_check;
ALTER TABLE conductor_wakes ADD CONSTRAINT conductor_wakes_kind_check
  CHECK (kind IN ('decision', 'escalation', 'question', 'safety', 'steer_read', 'steer_failed', 'pr_merged', 'pr_closed',
                  'published', 'publish_refused', 'publish_stalled', 'checkout', 'stalled', 'unblocked'));
